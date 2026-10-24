//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/internal/middleware/global/idempotency"
	"cgin/internal/platform/cache"
)

// openIdempotencyCache 打开真实缓存客户端，供幂等存储与锁的集成验证使用。
func openIdempotencyCache(t *testing.T) cache.Store {
	t.Helper()

	cfg, err := config.Load()
	require.NoError(t, err, "加载配置失败")

	store, err := cache.Open(t.Context(), cfg.Cache)
	require.NoError(t, err, "请确认 REDIS_URL 可达")
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestIdempotencyCacheStorageRoundTrip 验证响应缓存的写入、读取与未命中语义。
func TestIdempotencyCacheStorageRoundTrip(t *testing.T) {
	storage := idempotency.NewCacheStorage(openIdempotencyCache(t))
	ctx := t.Context()
	key := "integration:" + uuid.NewString()

	missing, err := storage.Get(ctx, key)
	require.NoError(t, err)
	assert.Nil(t, missing)

	payload := []byte(`{"statusCode":201,"body":"b3JkZXI="}`)
	require.NoError(t, storage.Set(ctx, key, payload, 2*time.Second))

	loaded, err := storage.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, payload, loaded)
}

// TestIdempotencyCacheLockerMutualExclusion 验证跨实例互斥与安全释放。
func TestIdempotencyCacheLockerMutualExclusion(t *testing.T) {
	locker := idempotency.NewCacheLocker(openIdempotencyCache(t), 5*time.Second)
	key := "integration-lock:" + uuid.NewString()

	require.NoError(t, locker.Lock(t.Context(), key))

	waitCtx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, locker.Lock(waitCtx, key), context.DeadlineExceeded,
		"同一键的第二个持有者必须等待，直到上下文取消")

	require.NoError(t, locker.Unlock(t.Context(), key))

	reacquireCtx, reacquireCancel := context.WithTimeout(t.Context(), time.Second)
	defer reacquireCancel()
	require.NoError(t, locker.Lock(reacquireCtx, key))
	require.NoError(t, locker.Unlock(t.Context(), key))
}
