package platform_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/budget"
	"github.com/failsafe-go/failsafe-go/bulkhead"
	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/failsafe-go/failsafe-go/failsafehttp"
	"github.com/failsafe-go/failsafe-go/ratelimiter"
	"github.com/failsafe-go/failsafe-go/retrypolicy"
	"github.com/failsafe-go/failsafe-go/timeout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/resilience"
)

func TestResilience_RetrySuccess(t *testing.T) {
	t.Parallel()

	executor := resilience.NewNetworkExecutor(3, 5*time.Millisecond, 20*time.Millisecond)

	var attempts atomic.Int32
	err := resilience.Run(context.Background(), executor, func() error {
		val := attempts.Add(1)
		if val < 3 {
			return errors.New("temporary network error")
		}
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, int32(3), attempts.Load())
}

func TestResilience_RetryExhausted(t *testing.T) {
	t.Parallel()

	executor := resilience.NewNetworkExecutor(2, 5*time.Millisecond, 10*time.Millisecond)

	var attempts atomic.Int32
	expectedErr := errors.New("permanent failure")
	err := resilience.Run(context.Background(), executor, func() error {
		attempts.Add(1)
		return expectedErr
	})

	require.ErrorIs(t, err, expectedErr)
	assert.Equal(t, int32(2), attempts.Load())
}

func TestResilience_CircuitBreaker(t *testing.T) {
	t.Parallel()

	cb := resilience.NewCircuitBreaker[any](resilience.CircuitBreakerConfig{
		FailureThreshold: 2,
		Delay:            50 * time.Millisecond,
	})

	executor := failsafe.With(cb)

	badErr := errors.New("downstream service down")

	// 第 1 次失败
	_ = resilience.Run(context.Background(), executor, func() error {
		return badErr
	})
	// 第 2 次失败触发熔断
	_ = resilience.Run(context.Background(), executor, func() error {
		return badErr
	})

	assert.True(t, cb.IsOpen(), "断路器在达到连续2次失败后应自动开启")

	// 第 3 次调用应被熔断器直接拦截
	var invoked bool
	err := resilience.Run(context.Background(), executor, func() error {
		invoked = true
		return nil
	})

	assert.False(t, invoked, "熔断器开启后不应再穿透执行业务函数")
	require.ErrorIs(t, err, circuitbreaker.ErrOpen)
}

func TestResilience_GetGeneric(t *testing.T) {
	t.Parallel()

	policy := resilience.NewRetryPolicy[string](resilience.RetryPolicyConfig{
		MaxAttempts: 3,
		Delay:       5 * time.Millisecond,
	})
	executor := failsafe.With(policy)

	var attempts atomic.Int32
	result, err := resilience.Get(context.Background(), executor, func() (string, error) {
		if attempts.Add(1) < 2 {
			return "", errors.New("retryable call")
		}
		return "hello failsafe", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "hello failsafe", result)
	assert.Equal(t, int32(2), attempts.Load())
}

func TestResilience_ContextCancellation(t *testing.T) {
	t.Parallel()

	executor := resilience.NewNetworkExecutor(5, 50*time.Millisecond, 100*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	err := resilience.Run(ctx, executor, func() error {
		return errors.New("always fail")
	})

	require.Error(t, err)
}

func TestResilience_RunWithContext(t *testing.T) {
	t.Parallel()

	t.Run("nil executor falls back to direct invocation", func(t *testing.T) {
		t.Parallel()
		var invoked bool
		err := resilience.RunWithContext[any](context.Background(), nil, func(ctx context.Context) error {
			assert.NotNil(t, ctx)
			invoked = true
			return nil
		})
		require.NoError(t, err)
		assert.True(t, invoked)
	})

	t.Run("propagates timeout context to inner closure", func(t *testing.T) {
		t.Parallel()
		to := resilience.NewTimeout[any](30 * time.Millisecond)
		executor := failsafe.With(to)

		var innerCanceled bool
		err := resilience.RunWithContext(context.Background(), executor, func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				innerCanceled = true
				return ctx.Err()
			case <-time.After(150 * time.Millisecond):
				return nil
			}
		})

		require.Error(t, err)
		assert.True(t, innerCanceled, "inner context should be canceled when timeout triggers")
	})
}

func TestResilience_GetWithContext(t *testing.T) {
	t.Parallel()

	t.Run("nil executor falls back to direct invocation", func(t *testing.T) {
		t.Parallel()
		res, err := resilience.GetWithContext(context.Background(), nil, func(ctx context.Context) (string, error) {
			assert.NotNil(t, ctx)
			return "fallback", nil
		})
		require.NoError(t, err)
		assert.Equal(t, "fallback", res)
	})

	t.Run("propagates timeout context to inner closure with generic result", func(t *testing.T) {
		t.Parallel()
		to := resilience.NewTimeout[string](30 * time.Millisecond)
		executor := failsafe.With(to)

		var innerCanceled bool
		res, err := resilience.GetWithContext(context.Background(), executor, func(ctx context.Context) (string, error) {
			select {
			case <-ctx.Done():
				innerCanceled = true
				return "", ctx.Err()
			case <-time.After(150 * time.Millisecond):
				return "done", nil
			}
		})

		require.Error(t, err)
		assert.Empty(t, res)
		assert.True(t, innerCanceled, "inner context should receive cancellation signal")
	})
}

func TestResilience_TypedExecutorWithRun(t *testing.T) {
	t.Parallel()

	// 验证类型化执行器（非 Executor[any]）也可直接传给 Run 与 RunWithContext
	policy := resilience.NewRetryPolicy[string](resilience.RetryPolicyConfig{
		MaxAttempts: 2,
		Delay:       5 * time.Millisecond,
	})
	typedExecutor := failsafe.With(policy)

	var runInvoked bool
	err := resilience.Run(context.Background(), typedExecutor, func() error {
		runInvoked = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, runInvoked)

	var runWithCtxInvoked bool
	err = resilience.RunWithContext(context.Background(), typedExecutor, func(ctx context.Context) error {
		assert.NotNil(t, ctx)
		runWithCtxInvoked = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, runWithCtxInvoked)
}

func TestResilience_NilContextSafety(t *testing.T) {
	t.Parallel()

	// nil context 必须被防御式兜底为 context.Background()，不触发 panic
	executor := resilience.NewNetworkExecutor(1, 0, 0)
	var nilCtx context.Context

	assert.NotPanics(t, func() {
		_ = resilience.Run(nilCtx, executor, func() error { return nil })
	})

	assert.NotPanics(t, func() {
		_ = resilience.RunWithContext(nilCtx, executor, func(ctx context.Context) error {
			assert.NotNil(t, ctx)
			return nil
		})
	})

	assert.NotPanics(t, func() {
		_, _ = resilience.Get(nilCtx, executor, func() (any, error) { return "ok", nil })
	})

	assert.NotPanics(t, func() {
		_, _ = resilience.GetWithContext(nilCtx, executor, func(ctx context.Context) (any, error) {
			assert.NotNil(t, ctx)
			return "ok", nil
		})
	})
}

func TestResilience_BulkheadZeroDefault(t *testing.T) {
	t.Parallel()

	bh := resilience.NewBulkhead[any](resilience.BulkheadConfig{
		MaxConcurrency: 0, // 零值应自动兜底为默认并发度 10
	})
	require.NotNil(t, bh)
}

func TestResilience_RateLimiter_Bursty(t *testing.T) {
	t.Parallel()

	rl := resilience.NewRateLimiter[any](resilience.RateLimiterConfig{
		MaxRate: 2,
		Period:  time.Second,
	})
	executor := resilience.NewExecutor(rl)

	// 前 2 次调用允许
	require.NoError(t, resilience.Run(context.Background(), executor, func() error { return nil }))
	require.NoError(t, resilience.Run(context.Background(), executor, func() error { return nil }))

	// 第 3 次调用立即超出速率限制
	err := resilience.Run(context.Background(), executor, func() error { return nil })
	require.Error(t, err)
	assert.ErrorIs(t, err, resilience.ErrRateLimitExceeded)
}

func TestResilience_RateLimiter_Smooth(t *testing.T) {
	t.Parallel()

	rl := resilience.NewRateLimiter[any](resilience.RateLimiterConfig{
		MaxRate: 10,
		Period:  time.Second,
		Smooth:  true,
	})
	executor := resilience.NewExecutor(rl)

	require.NoError(t, resilience.Run(context.Background(), executor, func() error { return nil }))
}

func TestResilience_RateLimiter_ZeroDefaults(t *testing.T) {
	t.Parallel()

	rl := resilience.NewRateLimiter[any](resilience.RateLimiterConfig{
		MaxRate: 0,
		Period:  0,
	})
	require.NotNil(t, rl)
}

func TestResilience_RateLimiter_MaxWaitTime(t *testing.T) {
	t.Parallel()

	rl := resilience.NewRateLimiter[any](resilience.RateLimiterConfig{
		MaxRate:     1,
		Period:      50 * time.Millisecond,
		MaxWaitTime: 200 * time.Millisecond,
	})
	executor := resilience.NewExecutor(rl)

	require.NoError(t, resilience.Run(context.Background(), executor, func() error { return nil }))

	// 第二次由于 MaxWaitTime 允许排队等待，稍后成功
	start := time.Now()
	require.NoError(t, resilience.Run(context.Background(), executor, func() error { return nil }))
	assert.GreaterOrEqual(t, time.Since(start), 40*time.Millisecond)
}

func TestResilience_Fallback_WithResult(t *testing.T) {
	t.Parallel()

	fb := resilience.NewFallbackWithResult("cached-value")
	executor := resilience.NewExecutor(fb)

	val, err := resilience.Get(context.Background(), executor, func() (string, error) {
		return "", errors.New("primary service unavailable")
	})

	require.NoError(t, err)
	assert.Equal(t, "cached-value", val)
}

func TestResilience_Fallback_WithFunc(t *testing.T) {
	t.Parallel()

	fb := resilience.NewFallbackWithFunc(func(exec failsafe.Execution[string]) (string, error) {
		return "recovered from: " + exec.LastError().Error(), nil
	})
	executor := resilience.NewExecutor(fb)

	val, err := resilience.Get(context.Background(), executor, func() (string, error) {
		return "", errors.New("database timeout")
	})

	require.NoError(t, err)
	assert.Equal(t, "recovered from: database timeout", val)
}

func TestResilience_HedgePolicy(t *testing.T) {
	t.Parallel()

	hp := resilience.NewHedgePolicy[string](resilience.HedgePolicyConfig{
		Delay:     20 * time.Millisecond,
		MaxHedges: 2,
	})
	executor := resilience.NewExecutor(hp)

	var attempts atomic.Int32
	val, err := resilience.GetWithContext(context.Background(), executor, func(ctx context.Context) (string, error) {
		att := attempts.Add(1)
		if att == 1 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(150 * time.Millisecond):
				return "slow-first-call", nil
			}
		}
		return "fast-hedged-call", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "fast-hedged-call", val)
	assert.GreaterOrEqual(t, attempts.Load(), int32(2))
}

func TestResilience_HedgePolicy_ZeroDefaults(t *testing.T) {
	t.Parallel()

	hp := resilience.NewHedgePolicy[any](resilience.HedgePolicyConfig{})
	require.NotNil(t, hp)
}

func TestResilience_Budget(t *testing.T) {
	t.Parallel()

	b := resilience.NewBudget(resilience.BudgetConfig{
		MaxRate:        0.2,
		MinConcurrency: 1,
	})
	require.NotNil(t, b)

	rp := resilience.NewRetryPolicy[any](resilience.RetryPolicyConfig{
		MaxAttempts: 2,
		Delay:       5 * time.Millisecond,
		Budget:      b,
	})
	executor := failsafe.With(rp)

	var attempts atomic.Int32
	err := resilience.Run(context.Background(), executor, func() error {
		if attempts.Add(1) < 2 {
			return errors.New("retry me")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), attempts.Load())
}

func TestResilience_HTTPRoundTripper(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		val := attempts.Add(1)
		if val == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	// 构建在 500 状态码时重试的 RetryPolicy
	retry := failsafehttp.NewRetryPolicyBuilder().
		HandleIf(func(resp *http.Response, _ error) bool {
			return resp != nil && resp.StatusCode >= 500
		}).
		WithMaxAttempts(2).
		Build()

	transport := resilience.NewHTTPRoundTripper(nil, retry)
	client := &http.Client{Transport: transport}

	resp, err := client.Get(ts.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok", string(body))
	assert.Equal(t, int32(2), attempts.Load())
}

func TestResilience_SentinelErrors(t *testing.T) {
	t.Parallel()

	assert.Equal(t, circuitbreaker.ErrOpen, resilience.ErrCircuitBreakerOpen)
	assert.Equal(t, timeout.ErrExceeded, resilience.ErrTimeoutExceeded)
	assert.Equal(t, bulkhead.ErrFull, resilience.ErrBulkheadFull)
	assert.Equal(t, retrypolicy.ErrExceeded, resilience.ErrRetriesExceeded)
	assert.Equal(t, ratelimiter.ErrExceeded, resilience.ErrRateLimitExceeded)
	assert.Equal(t, budget.ErrExceeded, resilience.ErrBudgetExceeded)
}

func TestResilience_NewResilientHTTPClient_Retry(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		val := attempts.Add(1)
		if val < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("resilient-ok"))
	}))
	defer ts.Close()

	client := resilience.NewResilientHTTPClient(resilience.ResilientTransportConfig{
		MaxAttempts:  3,
		InitialDelay: 5 * time.Millisecond,
		MaxDelay:     20 * time.Millisecond,
	})

	resp, err := client.Get(ts.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "resilient-ok", string(body))
	assert.Equal(t, int32(3), attempts.Load())
}

func TestResilience_NewResilientHTTPClient_CircuitBreaker(t *testing.T) {
	t.Parallel()

	var stateChanged atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := resilience.NewResilientHTTPClient(resilience.ResilientTransportConfig{
		MaxAttempts:  1, // 不重试，直接驱动断路器
		InitialDelay: 5 * time.Millisecond,
		CircuitBreaker: &resilience.CircuitBreakerConfig{
			FailureThreshold: 2,
			Delay:            100 * time.Millisecond,
			OnStateChanged: func(event circuitbreaker.StateChangedEvent) {
				if event.NewState == circuitbreaker.OpenState {
					stateChanged.Store(true)
				}
			},
		},
	})

	// 第 1 次 500
	resp1, err1 := client.Get(ts.URL)
	if err1 == nil {
		_ = resp1.Body.Close()
	}

	// 第 2 次 500，应触发断路器开启
	resp2, err2 := client.Get(ts.URL)
	if err2 == nil {
		_ = resp2.Body.Close()
	}

	assert.True(t, stateChanged.Load(), "断路器在达到连续2次500失败后应自动开启")

	// 第 3 次请求应被断路器直接拦截熔断
	_, err3 := client.Get(ts.URL)
	require.Error(t, err3)
	assert.ErrorIs(t, err3, circuitbreaker.ErrOpen)
}

func TestResilience_NewResilientHTTPClient_Bulkhead(t *testing.T) {
	t.Parallel()

	blocker := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-blocker
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	defer close(blocker)

	client := resilience.NewResilientHTTPClient(resilience.ResilientTransportConfig{
		MaxAttempts: 1,
		Bulkhead: &resilience.BulkheadConfig{
			MaxConcurrency: 1,
			MaxWaitTime:    10 * time.Millisecond,
		},
	})

	// 启动第一个请求占满并发槽
	go func() {
		resp, _ := client.Get(ts.URL)
		if resp != nil {
			_ = resp.Body.Close()
		}
	}()

	time.Sleep(20 * time.Millisecond)

	// 第二个请求在超时后应被隔离舱拦截
	_, err := client.Get(ts.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, bulkhead.ErrFull)
}

func TestResilience_NewResilientHTTPClient_RetryAfter_DateAndSeconds(t *testing.T) {
	t.Parallel()

	// 1. 验证 HTTP-date 格式超期时触发 AbortIf 快速熔断失败，不无谓阻塞
	futureDate := time.Now().Add(10 * time.Minute).UTC().Format(http.TimeFormat)
	tsDate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", futureDate)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer tsDate.Close()

	client := resilience.NewResilientHTTPClient(resilience.ResilientTransportConfig{
		MaxAttempts:  3,
		InitialDelay: 5 * time.Millisecond,
		MaxDelay:     20 * time.Millisecond,
	})

	start := time.Now()
	respDate, err := client.Get(tsDate.URL)
	require.NoError(t, err)
	defer func() { _ = respDate.Body.Close() }()

	// 应当在几毫秒内立即快速返回 429，而不是阻塞 10 分钟
	assert.Equal(t, http.StatusTooManyRequests, respDate.StatusCode)
	assert.Less(t, time.Since(start), 200*time.Millisecond)

	// 2. 验证 HTTP-date 格式为过去时间（负延迟）时降级返回 0 并正常执行退避重试
	attemptsPast := 0
	pastDate := time.Now().Add(-10 * time.Minute).UTC().Format(http.TimeFormat)
	tsPastDate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attemptsPast++
		if attemptsPast == 1 {
			w.Header().Set("Retry-After", pastDate)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer tsPastDate.Close()

	respPast, err := client.Get(tsPastDate.URL)
	require.NoError(t, err)
	defer func() { _ = respPast.Body.Close() }()
	assert.Equal(t, http.StatusOK, respPast.StatusCode)
	assert.Equal(t, 2, attemptsPast)

	// 3. 验证整数秒格式（例如 120 秒）超过 MaxDelay 时触发快速中止
	tsSeconds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer tsSeconds.Close()

	respSec, err := client.Get(tsSeconds.URL)
	require.NoError(t, err)
	defer func() { _ = respSec.Body.Close() }()
	assert.Equal(t, http.StatusTooManyRequests, respSec.StatusCode)
}
