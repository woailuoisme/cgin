//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/cache"
)

// TestTieredCache_MultiInstance_Invalidation 验证两实例节点在真实 Redis 上的 L1 读与主动自失效通知。
func TestTieredCache_MultiInstance_Invalidation(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err)

	ctx := t.Context()

	// 节点 1
	c1, err := cache.Open(ctx, cfg.Cache)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c1.Close() })

	l1Node1 := cache.NewMemoryStore()
	node1 := cache.NewTieredStore(l1Node1, c1, cache.WithTieredL1TTL(time.Hour))
	t.Cleanup(func() { _ = node1.Close() })

	// 节点 2
	c2, err := cache.Open(ctx, cfg.Cache)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c2.Close() })

	l1Node2 := cache.NewMemoryStore()
	node2 := cache.NewTieredStore(l1Node2, c2, cache.WithTieredL1TTL(time.Hour))
	t.Cleanup(func() { _ = node2.Close() })

	// 等待 Pub/Sub 订阅在 Redis 服务端建立完毕
	time.Sleep(100 * time.Millisecond)

	key := fmt.Sprintf("tests:tiered:sync:%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = node1.Delete(context.Background(), key) })

	// 1. 节点 1 写入键
	require.NoError(t, node1.Set(ctx, key, "initial-value", 5*time.Minute))

	// 2. 节点 2 读取键，发生 L1 Miss，从 L2 回填至节点 2 的 L1
	val, err := node2.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "initial-value", val)

	// 验证已确实缓存在节点 2 的本地 L1 内存中
	l1Val, err := l1Node2.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "initial-value", l1Val)

	// 3. 节点 1 更新键为新值
	require.NoError(t, node1.Set(ctx, key, "updated-value", 5*time.Minute))

	// 4. 等待 Pub/Sub 广播到达节点 2 并驱逐其 L1 缓存
	require.Eventually(t, func() bool {
		_, l1Err := l1Node2.Get(ctx, key)
		return l1Err != nil // L1 应已被驱逐为 NotFound
	}, 2*time.Second, 50*time.Millisecond, "节点 2 的本地 L1 缓存未能在超时时间内被驱逐")

	// 5. 节点 2 再次读取，应穿透读取到 L2 的最新值并回填
	val2, err := node2.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "updated-value", val2)

	// 6. 节点 1 删除键
	_, err = node1.Delete(ctx, key)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, l1Err := l1Node2.Get(ctx, key)
		return l1Err != nil
	}, 2*time.Second, 50*time.Millisecond)

	_, err = node2.Get(ctx, key)
	require.ErrorIs(t, err, cache.ErrNotFound)
}

// TestTieredCache_Remember_Fallback 验证 Remember 在两级缓存下的回源与 L1 填充。
func TestTieredCache_Remember_Fallback(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err)

	ctx := t.Context()
	c, err := cache.Open(ctx, cfg.Cache)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	l1 := cache.NewMemoryStore()
	tiered := cache.NewTieredStore(l1, c, cache.WithTieredL1TTL(time.Hour))
	t.Cleanup(func() { _ = tiered.Close() })

	key := fmt.Sprintf("tests:tiered:remember:%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = tiered.Delete(context.Background(), key) })

	var loadCount atomic.Int64
	loader := func(c context.Context) (string, error) {
		loadCount.Add(1)
		return "origin-payload", nil
	}

	// 首次回源
	val, err := tiered.Remember(ctx, key, 5*time.Minute, loader)
	require.NoError(t, err)
	assert.Equal(t, "origin-payload", val)
	assert.Equal(t, int64(1), loadCount.Load())

	// 第二次获取直接走 L1
	val2, err := tiered.Remember(ctx, key, 5*time.Minute, loader)
	require.NoError(t, err)
	assert.Equal(t, "origin-payload", val2)
	assert.Equal(t, int64(1), loadCount.Load()) // 没有增加

	// 直接读 L1 确认存在
	l1Val, err := l1.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "origin-payload", l1Val)
}
