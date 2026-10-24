// 本文件验证就绪探针底座的并发调度、两级超时、状态汇总与 Panic 恢复守卫。
package ready_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/ready"
)

type stubPinger struct {
	ping func(ctx context.Context) error
}

func (s stubPinger) Ping(ctx context.Context) error {
	if s.ping != nil {
		return s.ping(ctx)
	}
	return nil
}

func TestAggregatorAllHealthy(t *testing.T) {
	agg := ready.New(
		ready.NewCheck("database", ready.PingerFunc(stubPinger{}), ready.WithCritical(true), ready.WithDriver("postgres")),
		ready.NewCheck("cache", ready.PingerFunc(stubPinger{}), ready.WithCritical(false), ready.WithDriver("valkey")),
	)

	result, errs := agg.Check(context.Background())
	require.Empty(t, errs)
	assert.Equal(t, ready.StatusReady, result.Status)
	assert.True(t, result.IsReady())
	assert.Equal(t, http.StatusOK, result.StatusCode())

	dbCheck, ok := result.Checks["database"]
	require.True(t, ok)
	assert.Equal(t, ready.CheckHealthy, dbCheck.Status)
	assert.Equal(t, "postgres", dbCheck.Driver)
	assert.True(t, dbCheck.Critical)
	assert.NotEmpty(t, dbCheck.Latency)

	cacheCheck, ok := result.Checks["cache"]
	require.True(t, ok)
	assert.Equal(t, ready.CheckHealthy, cacheCheck.Status)
	assert.Equal(t, "valkey", cacheCheck.Driver)
	assert.False(t, cacheCheck.Critical)
}

func TestAggregatorCriticalFailure(t *testing.T) {
	dbErr := errors.New("connection refused")
	agg := ready.New(
		ready.NewCheck("database", ready.PingerFunc(stubPinger{
			ping: func(context.Context) error { return dbErr },
		}), ready.WithCritical(true)),
		ready.NewCheck("cache", ready.PingerFunc(stubPinger{}), ready.WithCritical(false)),
	)

	result, errs := agg.Check(context.Background())
	require.Len(t, errs, 1)
	require.ErrorIs(t, errs["database"], dbErr)

	assert.Equal(t, ready.StatusNotReady, result.Status)
	assert.False(t, result.IsReady())
	assert.Equal(t, http.StatusServiceUnavailable, result.StatusCode())
	assert.Equal(t, ready.CheckUnreachable, result.Checks["database"].Status)
	assert.Equal(t, ready.CheckHealthy, result.Checks["cache"].Status)
}

func TestAggregatorDegradedFailure(t *testing.T) {
	cacheErr := errors.New("valkey cluster down")
	agg := ready.New(
		ready.NewCheck("database", ready.PingerFunc(stubPinger{}), ready.WithCritical(true)),
		ready.NewCheck("cache", ready.PingerFunc(stubPinger{
			ping: func(context.Context) error { return cacheErr },
		}), ready.WithCritical(false)),
	)

	result, errs := agg.Check(context.Background())
	require.Len(t, errs, 1)
	require.ErrorIs(t, errs["cache"], cacheErr)

	assert.Equal(t, ready.StatusDegraded, result.Status)
	assert.True(t, result.IsReady())
	assert.Equal(t, http.StatusOK, result.StatusCode())
	assert.Equal(t, ready.CheckHealthy, result.Checks["database"].Status)
	assert.Equal(t, ready.CheckDegraded, result.Checks["cache"].Status)
}

func TestAggregatorPanicRecovery(t *testing.T) {
	agg := ready.New(
		ready.NewCheck("database", ready.PingerFunc(stubPinger{
			ping: func(context.Context) error { panic("nil pointer dereference") },
		}), ready.WithCritical(true)),
	)

	result, errs := agg.Check(context.Background())
	require.Len(t, errs, 1)
	assert.Contains(t, errs["database"].Error(), "probe panic")
	assert.Equal(t, ready.StatusNotReady, result.Status)
	assert.Equal(t, ready.CheckUnreachable, result.Checks["database"].Status)
}

func TestAggregatorTimeout(t *testing.T) {
	agg := ready.New(
		ready.NewCheck("database", ready.PingerFunc(stubPinger{
			ping: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
		}), ready.WithCritical(true), ready.WithTimeout(20*time.Millisecond)),
	)

	result, errs := agg.Check(context.Background())
	require.Len(t, errs, 1)
	assert.Contains(t, errs["database"].Error(), "timed out")
	assert.Equal(t, ready.StatusNotReady, result.Status)
}

func TestAggregatorNilPinger(t *testing.T) {
	agg := ready.New(
		ready.NewCheck("database", ready.PingerFunc(nil), ready.WithCritical(true)),
	)

	result, errs := agg.Check(context.Background())
	require.Len(t, errs, 1)
	require.ErrorIs(t, errs["database"], ready.ErrProbeNotConfigured)
	assert.Equal(t, ready.StatusNotReady, result.Status)
}

func TestAggregatorConcurrency(t *testing.T) {
	start := time.Now()
	agg := ready.New(
		ready.NewCheck("probe1", func(ctx context.Context) error {
			time.Sleep(30 * time.Millisecond)
			return nil
		}),
		ready.NewCheck("probe2", func(ctx context.Context) error {
			time.Sleep(30 * time.Millisecond)
			return nil
		}),
	)

	result, errs := agg.Check(context.Background())
	require.Empty(t, errs)
	assert.Equal(t, ready.StatusReady, result.Status)
	elapsed := time.Since(start)
	// 两项并发执行，总耗时应小于 55ms（串行为 60ms+）
	assert.Less(t, elapsed, 55*time.Millisecond)
}

func TestAggregatorDisabledCheck(t *testing.T) {
	called := false
	agg := ready.New(
		ready.NewCheck(
			ready.ComponentDatabase,
			ready.PingerFunc(stubPinger{}),
			ready.WithCritical(true),
			ready.WithDriver("postgres"),
		),
		ready.NewCheck(
			ready.ComponentRealtime,
			func(ctx context.Context) error {
				called = true
				return errors.New("should not be called")
			},
			ready.WithCritical(false),
			ready.WithDriver("centrifugo"),
			ready.WithEnabled(func() bool { return false }),
		),
	)

	result, errs := agg.Check(context.Background())
	require.Empty(t, errs)
	assert.Equal(t, ready.StatusReady, result.Status)
	assert.False(t, called, "未启用的检查项不应执行探测函数")

	realtimeCheck, ok := result.Checks[ready.ComponentRealtime]
	require.True(t, ok)
	assert.Equal(t, ready.CheckDisabled, realtimeCheck.Status)
	assert.Equal(t, "centrifugo", realtimeCheck.Driver)
	assert.False(t, realtimeCheck.Critical)
	assert.Empty(t, realtimeCheck.Latency)
}

func TestAggregatorCacheDuration(t *testing.T) {
	var count atomic.Int32
	agg := ready.New(
		ready.NewCheck("database", func(ctx context.Context) error {
			count.Add(1)
			return nil
		}, ready.WithCritical(true)),
	)
	agg.SetCacheDuration(150 * time.Millisecond)

	// 第一次调用，必定触发真实探测
	res1, errs1 := agg.Check(context.Background())
	require.Empty(t, errs1)
	assert.Equal(t, ready.StatusReady, res1.Status)
	assert.Equal(t, int32(1), count.Load())

	// 缓存窗口内再次调用，防雪崩缓存生效，不应重复调用 Ping
	res2, errs2 := agg.Check(context.Background())
	require.Empty(t, errs2)
	assert.Equal(t, ready.StatusReady, res2.Status)
	assert.Equal(t, int32(1), count.Load(), "缓存期内应直接复用结果，不重复探测")

	// 等待缓存过期后再次调用，应触发第二次探测
	time.Sleep(160 * time.Millisecond)
	res3, errs3 := agg.Check(context.Background())
	require.Empty(t, errs3)
	assert.Equal(t, ready.StatusReady, res3.Status)
	assert.Equal(t, int32(2), count.Load(), "缓存过期后应重新探测")
}
