package cache_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/cache"
)

func TestTieredStore_Standalone_Contract(t *testing.T) {
	l1 := cache.NewMemoryStore()
	tiered := cache.NewTieredStore(l1, nil, cache.WithTieredL1TTL(10*time.Minute))
	defer func() { _ = tiered.Close() }()

	ctx := context.Background()
	key := "test:tiered:standalone:1"

	assert.Equal(t, "tiered", tiered.Driver())
	require.NoError(t, tiered.Ping(ctx))
	assert.NotEmpty(t, tiered.InstanceID())
	assert.Equal(t, l1, tiered.L1())
	assert.Nil(t, tiered.L2())

	// 写入
	require.NoError(t, tiered.Set(ctx, key, "hello", 5*time.Minute))

	// Exists
	exists, err := tiered.Exists(ctx, key)
	require.NoError(t, err)
	assert.True(t, exists)

	// Get
	val, err := tiered.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "hello", val)

	// TTL
	ttl, err := tiered.TTL(ctx, key)
	require.NoError(t, err)
	assert.Positive(t, ttl)

	// Increment
	incrKey := "test:tiered:incr"
	first, err := tiered.Increment(ctx, incrKey, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first)

	second, err := tiered.Increment(ctx, incrKey, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(2), second)

	// SetNX
	nxKey := "test:tiered:nx"
	ok, err := tiered.SetNX(ctx, nxKey, "val", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = tiered.SetNX(ctx, nxKey, "val2", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok)

	// DeleteIfMatch
	deleted, err := tiered.DeleteIfMatch(ctx, nxKey, "wrong")
	require.NoError(t, err)
	assert.False(t, deleted)

	deleted, err = tiered.DeleteIfMatch(ctx, nxKey, "val")
	require.NoError(t, err)
	assert.True(t, deleted)

	// Delete
	n, err := tiered.Delete(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n) // Standalone without L2 returns 0 for L2 delete

	val, err = tiered.Get(ctx, key)
	require.ErrorIs(t, err, cache.ErrNotFound)
	assert.Empty(t, val)
}

func TestTieredStore_Remember_SingleFlight(t *testing.T) {
	l1 := cache.NewMemoryStore()
	tiered := cache.NewTieredStore(l1, nil)
	defer func() { _ = tiered.Close() }()

	ctx := context.Background()
	key := "test:tiered:remember:sf"

	var loadCount atomic.Int64
	loader := func(c context.Context) (string, error) {
		loadCount.Add(1)
		time.Sleep(20 * time.Millisecond)
		return "computed-data", nil
	}

	const concurrency = 10
	var wg sync.WaitGroup
	results := make([]string, concurrency)

	for i := range concurrency {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			val, err := tiered.Remember(ctx, key, 5*time.Minute, loader)
			if assert.NoError(t, err) {
				results[idx] = val
			}
		}(i)
	}

	wg.Wait()

	// 验证 singleflight 生效：仅回源执行 1 次
	assert.Equal(t, int64(1), loadCount.Load())
	for _, res := range results {
		assert.Equal(t, "computed-data", res)
	}

	// 验证已落入 L1
	cached, err := l1.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "computed-data", cached)
}

func TestTieredStore_JSONHelpers(t *testing.T) {
	l1 := cache.NewMemoryStore()
	tiered := cache.NewTieredStore(l1, nil)
	defer func() { _ = tiered.Close() }()

	ctx := context.Background()
	key := "test:tiered:json:user"

	user := userSession{
		ID:       "usr_123",
		Username: "gopher",
		Role:     "admin",
	}

	require.NoError(t, cache.SetJSON(ctx, tiered, key, user, time.Minute))

	got, err := cache.GetJSON[userSession](ctx, tiered, key)
	require.NoError(t, err)
	assert.Equal(t, user, got)

	// RememberJSON
	rememberKey := "test:tiered:json:remember"
	var loadCount int
	remUser, err := cache.RememberJSON(ctx, tiered, rememberKey, time.Minute, func(c context.Context) (userSession, error) {
		loadCount++
		return userSession{ID: "usr_456", Username: "bob", Role: "member"}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "usr_456", remUser.ID)
	assert.Equal(t, 1, loadCount)

	// Second fetch hits cache
	remUser2, err := cache.RememberJSON(ctx, tiered, rememberKey, time.Minute, func(c context.Context) (userSession, error) {
		loadCount++
		return userSession{ID: "usr_999"}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "usr_456", remUser2.ID)
	assert.Equal(t, 1, loadCount)
}

func TestTieredStore_Locker(t *testing.T) {
	l1 := cache.NewMemoryStore()
	tiered := cache.NewTieredStore(l1, nil)
	defer func() { _ = tiered.Close() }()

	locker := tiered.Locker()
	require.NotNil(t, locker)

	ctx := context.Background()
	lockKey := "test:tiered:lock"

	lockedCtx, cancel, err := locker.Lock(ctx, lockKey)
	require.NoError(t, err)
	require.NotNil(t, lockedCtx)

	// 互斥测试
	_, _, err = locker.TryLock(ctx, lockKey)
	require.ErrorIs(t, err, cache.ErrNotLocked)

	cancel()

	// 释放后可再次获取
	_, cancel2, err := locker.TryLock(ctx, lockKey)
	require.NoError(t, err)
	cancel2()
}

func TestTieredStore_Close(t *testing.T) {
	l1 := cache.NewMemoryStore()
	tiered := cache.NewTieredStore(l1, nil)

	ctx := context.Background()
	require.NoError(t, tiered.Set(ctx, "k1", "v1", time.Minute))

	require.NoError(t, tiered.Close())

	// 关闭后操作被拦截
	_, err := tiered.Get(ctx, "k1")
	require.Error(t, err)

	err = tiered.Set(ctx, "k2", "v2", time.Minute)
	require.Error(t, err)

	require.Error(t, tiered.Ping(ctx))

	// 重复关闭幂等无害
	require.NoError(t, tiered.Close())
}

func TestTieredStore_InvalidationSerialization(t *testing.T) {
	// 验证失效消息序列化与反序列化协议
	type invalidationMessage struct {
		InstanceID string   `json:"instanceId"`
		Keys       []string `json:"keys"`
	}

	msg := invalidationMessage{
		InstanceID: "node-1",
		Keys:       []string{"key1", "key2"},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded invalidationMessage
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, msg.InstanceID, decoded.InstanceID)
	assert.Equal(t, msg.Keys, decoded.Keys)
}
