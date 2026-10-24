//go:build integration

// 本文件对着真实 Valkey/Redis 覆盖缓存封装的契约。
//
// 之前这个封装没有任何直接测试，只有集成链路间接碰到它。迁移客户端之后，几处语义必须
// 逐个钉住：键的 TTL 真的被设上、`Increment` 的固定窗口不会因后续自增被延长、
// 键不存在与没有过期时间这两种"负值"不会被混淆。
package integration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/cache"
)

// newCacheClient 用当前环境的 REDIS_URL 建立连接，并登记关闭。
func newCacheClient(t *testing.T) *cache.Client {
	t.Helper()

	cfg, err := config.Load()
	require.NoError(t, err, "加载配置失败")

	client, err := cache.Open(t.Context(), cfg.Cache)
	require.NoError(t, err, "请确认 REDIS_URL 可达")
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// cacheKey 生成带时间戳的隔离键，并登记删除，避免污染真实键空间。
func cacheKey(t *testing.T, client *cache.Client, suffix string) string {
	t.Helper()

	key := fmt.Sprintf("tests:cache:%s:%d", suffix, time.Now().UnixNano())
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), key) })
	return key
}

// TestCacheContract 覆盖写入、探测、TTL 与删除的基本往返。
func TestCacheContract(t *testing.T) {
	client := newCacheClient(t)
	ctx := t.Context()
	key := cacheKey(t, client, "basic")

	require.NoError(t, client.Set(ctx, key, "1", time.Minute))

	exists, err := client.Exists(ctx, key)
	require.NoError(t, err)
	require.True(t, exists)

	ttl, err := client.TTL(ctx, key)
	require.NoError(t, err)
	assert.Positive(t, ttl, "写入的键必须带上过期时间")
	assert.LessOrEqual(t, ttl, time.Minute)

	// ttl 非正数必须被拒绝，否则会写进一个永不过期的键。
	require.Error(t, client.Set(ctx, key, "1", 0))

	deleted, err := client.Delete(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	// 删除不存在的键：数量为 0，且不报错。
	deleted, err = client.Delete(ctx, key)
	require.NoError(t, err)
	assert.Zero(t, deleted)

	exists, err = client.Exists(ctx, key)
	require.NoError(t, err)
	assert.False(t, exists)

	// 键不存在时 TTL 归一为 0，而不是把服务端的 -2 透出去。
	ttl, err = client.TTL(ctx, key)
	require.NoError(t, err)
	assert.Zero(t, ttl)
}

// TestCacheIncrementKeepsWindow 锁定固定窗口计数的关键性质：首次自增同时设上过期时间，
// 后续自增只累加、不重置窗口。
//
// 若这条坏掉，计数键会永久留在库里，那个客户端就被恒定限流——是限流器最难排查的一类故障。
func TestCacheIncrementKeepsWindow(t *testing.T) {
	client := newCacheClient(t)
	ctx := t.Context()
	key := cacheKey(t, client, "window")
	window := 2 * time.Minute

	first, err := client.Increment(ctx, key, window)
	require.NoError(t, err)
	require.Equal(t, int64(1), first)

	ttl, err := client.TTL(ctx, key)
	require.NoError(t, err)
	assert.Positive(t, ttl, "首次自增必须同时设上过期时间")
	assert.LessOrEqual(t, ttl, window)

	second, err := client.Increment(ctx, key, window)
	require.NoError(t, err)
	require.Equal(t, int64(2), second)

	ttlAfter, err := client.TTL(ctx, key)
	require.NoError(t, err)
	assert.LessOrEqual(t, ttlAfter, ttl, "后续自增不应延长窗口")

	// ttl 非正数必须被拒绝。
	_, err = client.Increment(ctx, key, 0)
	assert.Error(t, err)
}

// TestCacheGetAndSetNX 覆盖读取与"仅当不存在时写入"的原语。
func TestCacheGetAndSetNX(t *testing.T) {
	client := newCacheClient(t)
	ctx := t.Context()
	key := cacheKey(t, client, "getnx")

	// 未命中必须是可判别的 ErrNotFound，而不是任意错误。
	_, err := client.Get(ctx, key)
	require.ErrorIs(t, err, cache.ErrNotFound)

	require.NoError(t, client.Set(ctx, key, "first", time.Minute))
	value, err := client.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "first", value)

	// 键已存在：SetNX 不写入，也不改动原值。
	acquired, err := client.SetNX(ctx, key, "second", time.Minute)
	require.NoError(t, err)
	assert.False(t, acquired)

	value, err = client.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "first", value, "SetNX 失败时不能覆盖已有值")

	// 换一个键：SetNX 成功且值可读回。
	freshKey := cacheKey(t, client, "getnx-fresh")
	acquired, err = client.SetNX(ctx, freshKey, "only-once", time.Minute)
	require.NoError(t, err)
	assert.True(t, acquired)

	value, err = client.Get(ctx, freshKey)
	require.NoError(t, err)
	assert.Equal(t, "only-once", value)

	// ttl 非正数必须被拒绝。
	_, err = client.SetNX(ctx, freshKey, "x", 0)
	assert.Error(t, err)
}

// TestCacheRemember 覆盖 cache-aside：命中不再回源、未命中回源并缓存、回源失败向上传递。
func TestCacheRemember(t *testing.T) {
	client := newCacheClient(t)
	ctx := t.Context()
	key := cacheKey(t, client, "remember")

	var calls int
	load := func(context.Context) (string, error) {
		calls++
		return "loaded", nil
	}

	value, err := client.Remember(ctx, key, time.Minute, load)
	require.NoError(t, err)
	assert.Equal(t, "loaded", value)
	assert.Equal(t, 1, calls)

	// 第二次应当直接命中缓存。
	value, err = client.Remember(ctx, key, time.Minute, load)
	require.NoError(t, err)
	assert.Equal(t, "loaded", value)
	assert.Equal(t, 1, calls, "命中缓存时不应再回源")

	// 回源失败：错误向上传递，且不留下缓存。
	failingKey := cacheKey(t, client, "remember-fail")
	loadErr := errors.New("origin load failed")
	_, err = client.Remember(ctx, failingKey, time.Minute, func(context.Context) (string, error) {
		return "", loadErr
	})
	require.ErrorIs(t, err, loadErr)

	_, err = client.Get(ctx, failingKey)
	assert.ErrorIs(t, err, cache.ErrNotFound, "回源失败不应写入缓存")
}

// TestCacheRememberDeduplicatesConcurrentLoads 锁定 singleflight 的效果：
// 同一个键上的并发未命中只应回源一次，否则缓存击穿会原样穿透到下游。
func TestCacheRememberDeduplicatesConcurrentLoads(t *testing.T) {
	client := newCacheClient(t)
	key := cacheKey(t, client, "singleflight")

	var (
		calls      atomic.Int64
		waiters    = 8
		values     = make([]string, waiters)
		seenErrors = make([]error, waiters)
		wg         sync.WaitGroup
	)

	load := func(context.Context) (string, error) {
		calls.Add(1)
		// 让并发的调用方真的撞在一起，否则它们会依次命中缓存而测不到去重。
		time.Sleep(50 * time.Millisecond)
		return "shared", nil
	}

	for i := range waiters {
		wg.Go(func() {
			values[i], seenErrors[i] = client.Remember(t.Context(), key, time.Minute, load)
		})
	}
	wg.Wait()

	for i := range waiters {
		require.NoError(t, seenErrors[i])
		assert.Equal(t, "shared", values[i])
	}
	assert.Equal(t, int64(1), calls.Load(), "并发未命中只应回源一次")
}

// TestCacheHonoursCallerContext 确认调用方的 ctx 真的被使用：已取消的 ctx 不能变成一次
// 正常读写。每操作预算只是上限，不会延长调用方给的更短期限。
func TestCacheHonoursCallerContext(t *testing.T) {
	client := newCacheClient(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := client.Get(ctx, cache.Key("tests", "cancelled"))
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
