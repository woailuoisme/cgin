package cache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/cache"
)

type userSession struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func TestMemoryStore_Contract(t *testing.T) {
	store := cache.NewMemoryStore()
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	key := "test:basic:1"

	assert.Equal(t, "memory", store.Driver())
	require.NoError(t, store.Ping(ctx))

	// 写入带过期时间的键
	require.NoError(t, store.Set(ctx, key, "val1", 5*time.Minute))

	exists, err := store.Exists(ctx, key)
	require.NoError(t, err)
	assert.True(t, exists)

	val, err := store.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "val1", val)

	ttl, err := store.TTL(ctx, key)
	require.NoError(t, err)
	assert.Positive(t, ttl)
	assert.LessOrEqual(t, ttl, 5*time.Minute)

	// 非正数 TTL 拒绝写入
	err = store.Set(ctx, key, "val2", 0)
	require.Error(t, err)

	// 删除
	delCount, err := store.Delete(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, int64(1), delCount)

	// 删除不存在的键
	delCount, err = store.Delete(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, int64(0), delCount)

	exists, err = store.Exists(ctx, key)
	require.NoError(t, err)
	assert.False(t, exists)

	ttl, err = store.TTL(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), ttl)
}

func TestMemoryStore_IncrementKeepsWindow(t *testing.T) {
	store := cache.NewMemoryStore()
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	key := "test:incr:window"
	window := 2 * time.Minute

	// 首次自增创建键并设定窗口
	first, err := store.Increment(ctx, key, window)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first)

	ttl, err := store.TTL(ctx, key)
	require.NoError(t, err)
	assert.Positive(t, ttl)
	assert.LessOrEqual(t, ttl, window)

	// 第二次自增只累加，不延长原窗口
	second, err := store.Increment(ctx, key, window)
	require.NoError(t, err)
	assert.Equal(t, int64(2), second)

	ttlAfter, err := store.TTL(ctx, key)
	require.NoError(t, err)
	assert.LessOrEqual(t, ttlAfter, ttl)

	// 非正数拒绝
	_, err = store.Increment(ctx, key, 0)
	require.Error(t, err)
}

func TestMemoryStore_GetAndSetNX(t *testing.T) {
	store := cache.NewMemoryStore()
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	key := "test:setnx:key"

	// 未命中必须返回 ErrNotFound
	_, err := store.Get(ctx, key)
	require.ErrorIs(t, err, cache.ErrNotFound)

	// 首次 SetNX 成功
	ok, err := store.SetNX(ctx, key, "first_val", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)

	val, err := store.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "first_val", val)

	// 键已存在时 SetNX 失败且不覆盖原值
	ok, err = store.SetNX(ctx, key, "second_val", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok)

	val, err = store.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "first_val", val)
}

func TestMemoryStore_Remember(t *testing.T) {
	store := cache.NewMemoryStore()
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	key := "test:remember:key"

	var callCount int
	loadFunc := func(context.Context) (string, error) {
		callCount++
		return "origin_data", nil
	}

	// 第一次回源
	val, err := store.Remember(ctx, key, time.Minute, loadFunc)
	require.NoError(t, err)
	assert.Equal(t, "origin_data", val)
	assert.Equal(t, 1, callCount)

	// 第二次命中缓存
	val, err = store.Remember(ctx, key, time.Minute, loadFunc)
	require.NoError(t, err)
	assert.Equal(t, "origin_data", val)
	assert.Equal(t, 1, callCount)

	// 回源失败向上传递且不缓存
	failKey := "test:remember:fail"
	expectedErr := errors.New("load error")
	_, err = store.Remember(ctx, failKey, time.Minute, func(context.Context) (string, error) {
		return "", expectedErr
	})
	require.ErrorIs(t, err, expectedErr)

	_, err = store.Get(ctx, failKey)
	assert.ErrorIs(t, err, cache.ErrNotFound)
}

func TestMemoryStore_RememberDeduplicatesConcurrentLoads(t *testing.T) {
	store := cache.NewMemoryStore()
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	key := "test:singleflight:key"

	var (
		calls   atomic.Int64
		waiters = 8
		values  = make([]string, waiters)
		errs    = make([]error, waiters)
		wg      sync.WaitGroup
	)

	load := func(context.Context) (string, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return "concurrent_result", nil
	}

	for i := range waiters {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			values[idx], errs[idx] = store.Remember(ctx, key, time.Minute, load)
		}(i)
	}
	wg.Wait()

	for i := range waiters {
		require.NoError(t, errs[i])
		assert.Equal(t, "concurrent_result", values[i])
	}
	assert.Equal(t, int64(1), calls.Load(), "并发请求应被 singleflight 去重仅回源一次")
}

func TestMemoryStore_JSONHelpers(t *testing.T) {
	store := cache.NewMemoryStore()
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	key := "user:session:123"

	session := userSession{
		ID:       "sess_abc",
		Username: "alice",
		Role:     "admin",
	}

	// 1. SetJSON & GetJSON
	err := cache.SetJSON(ctx, store, key, session, time.Minute)
	require.NoError(t, err)

	got, err := cache.GetJSON[userSession](ctx, store, key)
	require.NoError(t, err)
	assert.Equal(t, session, got)

	// 2. RememberJSON
	rememberKey := "user:session:456"
	var loadCalls int
	remembered, err := cache.RememberJSON(ctx, store, rememberKey, time.Minute, func(context.Context) (userSession, error) {
		loadCalls++
		return userSession{ID: "sess_456", Username: "bob", Role: "member"}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "bob", remembered.Username)
	assert.Equal(t, 1, loadCalls)

	// 命中缓存
	remembered2, err := cache.RememberJSON(ctx, store, rememberKey, time.Minute, func(context.Context) (userSession, error) {
		loadCalls++
		return userSession{}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "bob", remembered2.Username)
	assert.Equal(t, 1, loadCalls)
}

func TestMemoryStore_ContextCancellation(t *testing.T) {
	store := cache.NewMemoryStore()
	defer func() { _ = store.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.Get(ctx, "cancelled_key")
	require.ErrorIs(t, err, context.Canceled)

	err = store.Set(ctx, "cancelled_key", "val", time.Minute)
	require.ErrorIs(t, err, context.Canceled)
}

func TestMemoryStore_CapacityEviction(t *testing.T) {
	// 创建小容量缓存验证 W-TinyLFU 驱逐
	store := cache.NewMemoryStoreWithCapacity(5)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	for i := range 10 {
		key := "evict:key:" + string(rune('a'+i))
		err := store.Set(ctx, key, "val", time.Minute)
		require.NoError(t, err)
	}

	// 确认总数不会无限制膨胀超过 capacity
	var aliveCount int
	for i := range 10 {
		key := "evict:key:" + string(rune('a'+i))
		if _, err := store.Get(ctx, key); err == nil {
			aliveCount++
		}
	}
	assert.LessOrEqual(t, aliveCount, 5, "内存缓存数量不应超过设定容量上限")
}
