// Package queue_test 验证任务队列基础设施与调度编排的核心契约。
package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"cgin/pkg/logger"
	"cgin/pkg/queue"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/redis/go-redis/v9/maintnotifications.(*CircuitBreakerManager).cleanupLoop"),
	)
}

type testPayload struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestTaskRouter_RegisterAndDispatch(t *testing.T) {
	t.Parallel()

	router := queue.NewTaskRouter()
	assert.Empty(t, router.Handlers())
	assert.Empty(t, router.TaskNames())

	executed := false
	queue.Register(router, "user:created", func(_ context.Context, p testPayload) error {
		executed = true
		assert.Equal(t, "Alice", p.Name)
		assert.Equal(t, 42, p.Count)
		return nil
	}, queue.WithoutOverlapping(5*time.Minute))

	// 验证路由表记录
	h, ok := router.GetHandler("user:created")
	require.True(t, ok)
	require.NotNil(t, h)

	ttl, locked := router.GetLockTTL("user:created")
	assert.True(t, locked)
	assert.Equal(t, 5*time.Minute, ttl)

	// 模拟解包调用
	err := h(context.Background(), []byte(`{"name":"Alice","count":42}`))
	require.NoError(t, err)
	assert.True(t, executed)

	// 畸形 JSON 报错
	err = h(context.Background(), []byte(`{invalid json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to unmarshal payload")

	// 边界检查：空任务名或 nil 处理器 panic
	assert.Panics(t, func() {
		router.Handle("", func(_ context.Context, _ []byte) error { return nil })
	})
	assert.Panics(t, func() {
		router.Handle("valid:name", nil)
	})
}

func TestQueue_ConfigNormalized(t *testing.T) {
	t.Parallel()

	cfg := queue.Config{}.Normalized()
	assert.Equal(t, "redis://127.0.0.1:6379", cfg.RedisURL)
	assert.Equal(t, queue.DefaultConcurrency, cfg.Concurrency)
	assert.Equal(t, queue.DefaultShutdownTimeout, cfg.ShutdownTimeout)

	custom := queue.Config{
		RedisURL:        "redis://10.0.0.1:6380",
		Concurrency:     25,
		ShutdownTimeout: 10 * time.Second,
	}.Normalized()
	assert.Equal(t, "redis://10.0.0.1:6380", custom.RedisURL)
	assert.Equal(t, 25, custom.Concurrency)
	assert.Equal(t, 10*time.Second, custom.ShutdownTimeout)
}

type mockLocker struct {
	acquireFn func(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error)
}

func (m *mockLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error) {
	if m.acquireFn != nil {
		return m.acquireFn(ctx, key, ttl)
	}
	return func(_ context.Context) error { return nil }, true, nil
}

func TestQueue_TryLock(t *testing.T) {
	t.Parallel()

	// 1. 无 Locker 时直接执行
	executed := false
	acquired, err := queue.TryLock(context.Background(), nil, "test:lock", time.Minute, func(_ context.Context) error {
		executed = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.True(t, executed)

	// 2. 正常获取锁并释放
	released := false
	locker := &mockLocker{
		acquireFn: func(_ context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error) {
			assert.Equal(t, "order:123", key)
			assert.Equal(t, time.Minute, ttl)
			return func(_ context.Context) error {
				released = true
				return nil
			}, true, nil
		},
	}

	executed = false
	acquired, err = queue.TryLock(context.Background(), locker, "order:123", time.Minute, func(_ context.Context) error {
		executed = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.True(t, executed)
	assert.True(t, released)

	// 3. 锁被占用时跳过执行
	occupiedLocker := &mockLocker{
		acquireFn: func(_ context.Context, _ string, _ time.Duration) (func(context.Context) error, bool, error) {
			return nil, false, nil
		},
	}
	executed = false
	acquired, err = queue.TryLock(context.Background(), occupiedLocker, "occupied:key", time.Minute, func(_ context.Context) error {
		executed = true
		return nil
	})
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.False(t, executed)
}

func TestScheduler_Entries(t *testing.T) {
	t.Parallel()

	sched, err := queue.NewScheduler(queue.Config{}, time.UTC, queue.WithSchedulerLogger(logger.Nop()))
	require.NoError(t, err)
	require.NotNil(t, sched)

	require.NoError(t, sched.Register("0 2 * * *", "db:cleanup", map[string]string{"days": "30"}, queue.WithQueue("low")))
	require.NoError(t, sched.Register("@daily", "report:daily", nil))

	entries := sched.Entries()
	require.Len(t, entries, 2)
	assert.Equal(t, "0 2 * * *", entries[0].Spec)
	assert.Equal(t, "db:cleanup", entries[0].TaskName)
	assert.Equal(t, "low", entries[0].Settings.Queue)
	assert.Equal(t, "@daily", entries[1].Spec)

	// 错误参数校验
	require.Error(t, sched.Register("", "task", nil))
	require.Error(t, sched.Register("0 * * * *", "", nil))
}

func TestQueue_NewComponents(t *testing.T) {
	t.Parallel()

	cfg := queue.Config{RedisURL: "redis://localhost:6379"}

	// 1. Producer
	producer, err := queue.NewProducer(cfg, queue.WithProducerLogger(logger.Nop()))
	require.NoError(t, err)
	require.NotNil(t, producer)
	require.NoError(t, producer.Close())

	// 2. Server
	srv, err := queue.NewServer(cfg, queue.WithServerLogger(logger.Nop()))
	require.NoError(t, err)
	require.NotNil(t, srv)
	require.NoError(t, srv.Shutdown())

	// 3. Inspector
	inspector, err := queue.NewInspector(cfg, queue.WithInspectorLogger(logger.Nop()))
	require.NoError(t, err)
	require.NotNil(t, inspector)
	require.NoError(t, inspector.Close())

	// 4. 非法 Redis URL
	badCfg := queue.Config{RedisURL: "://invalid url"}
	_, err = queue.NewProducer(badCfg)
	require.Error(t, err)
	_, err = queue.NewServer(badCfg)
	require.Error(t, err)
	_, err = queue.NewInspector(badCfg)
	require.Error(t, err)
	_, err = queue.NewScheduler(badCfg, time.UTC)
	require.Error(t, err)
}
