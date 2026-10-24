// Package resilience 基于 failsafe-go 封装通用的弹性容错机制（重试、熔断断路、隔离舱、限流整形、降级回退、长尾对冲、重试预算与传输层中间件）。
// 为全系统出站调用（三方支付、短信、邮件、对象存储与实时推送）提供统一的弹性防护底座。
package resilience

import (
	"cmp"
	"context"
	"net/http"
	"time"

	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/budget"
	"github.com/failsafe-go/failsafe-go/bulkhead"
	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/failsafe-go/failsafe-go/failsafehttp"
	"github.com/failsafe-go/failsafe-go/fallback"
	"github.com/failsafe-go/failsafe-go/hedgepolicy"
	"github.com/failsafe-go/failsafe-go/ratelimiter"
	"github.com/failsafe-go/failsafe-go/retrypolicy"
	"github.com/failsafe-go/failsafe-go/timeout"
	"github.com/samber/lo"
)

var (
	// ErrCircuitBreakerOpen 断路器处于开启状态（已熔断）。
	ErrCircuitBreakerOpen = circuitbreaker.ErrOpen
	// ErrTimeoutExceeded 操作执行已超时。
	ErrTimeoutExceeded = timeout.ErrExceeded
	// ErrBulkheadFull 隔离舱并发与等待队列已满。
	ErrBulkheadFull = bulkhead.ErrFull
	// ErrRetriesExceeded 重试策略已耗尽全部尝试次数。
	ErrRetriesExceeded = retrypolicy.ErrExceeded
	// ErrRateLimitExceeded 频率已超出配置配额限制。
	ErrRateLimitExceeded = ratelimiter.ErrExceeded
	// ErrBudgetExceeded 重试或对冲预算已耗尽。
	ErrBudgetExceeded = budget.ErrExceeded
)

// RetryPolicyConfig 重试策略配置项。
type RetryPolicyConfig struct {
	MaxAttempts int
	Delay       time.Duration
	MaxDelay    time.Duration
	Jitter      time.Duration
	Budget      budget.Budget // 可选配置的重试预算配额
}

// CircuitBreakerConfig 熔断断路器配置项。
type CircuitBreakerConfig struct {
	FailureThreshold uint
	SuccessThreshold uint
	Delay            time.Duration
	OnStateChanged   func(circuitbreaker.StateChangedEvent)
}

// BulkheadConfig 并发隔离舱配置项。
type BulkheadConfig struct {
	MaxConcurrency uint
	MaxWaitTime    time.Duration
}

// RateLimiterConfig 限流策略配置项。
type RateLimiterConfig struct {
	MaxRate     uint          // 周期内允许的最大调用数（0 兜底为 100）
	Period      time.Duration // 限流周期窗口（如 1 秒、1 分钟，<=0 兜底为 1 秒）
	MaxWaitTime time.Duration // 排队等待的最长耗时，超时快速失败并返回 ErrRateLimitExceeded（0 为不等待直接超限拦截）
	Smooth      bool          // 是否开启平滑漏桶模式（true 为 Leaky Bucket 均匀展开；false 为 Bursty 突发模式）
}

// HedgePolicyConfig 对冲请求策略配置项。
type HedgePolicyConfig struct {
	Delay     time.Duration // 发起对冲请求前的初始延迟等待（<=0 兜底为 50ms）
	MaxHedges int           // 最大并发发起的对冲请求数（<=0 兜底为 1）
	Budget    budget.Budget // 可选配置的对冲预算配额
}

// BudgetConfig 重试与对冲预算配置项。
type BudgetConfig struct {
	MaxRate        float64 // 允许用于重试/对冲的最大执行占比（0.0 ~ 1.0，如 0.2 表示最多 20%）
	MinConcurrency uint    // 保底并发执行配额
}

// NewRetryPolicy 构建带指数退避与重试上限的重试策略。
func NewRetryPolicy[R any](cfg RetryPolicyConfig) retrypolicy.RetryPolicy[R] {
	builder := retrypolicy.NewBuilder[R]()

	if cfg.MaxAttempts > 0 {
		builder = builder.WithMaxAttempts(cfg.MaxAttempts)
	}

	if cfg.Delay > 0 {
		if cfg.MaxDelay > cfg.Delay {
			builder = builder.WithBackoff(cfg.Delay, cfg.MaxDelay)
		} else {
			builder = builder.WithDelay(cfg.Delay)
		}
	}

	if cfg.Jitter > 0 {
		builder = builder.WithJitter(cfg.Jitter)
	}

	if cfg.Budget != nil {
		builder = builder.WithBudget(cfg.Budget)
	}

	return builder.Build()
}

// NewCircuitBreaker 构建熔断断路器，用于探测下游故障并主动切断请求防止雪崩。
func NewCircuitBreaker[R any](cfg CircuitBreakerConfig) circuitbreaker.CircuitBreaker[R] {
	builder := circuitbreaker.NewBuilder[R]()

	if cfg.FailureThreshold > 0 {
		builder = builder.WithFailureThreshold(cfg.FailureThreshold)
	}

	if cfg.SuccessThreshold > 0 {
		builder = builder.WithSuccessThreshold(cfg.SuccessThreshold)
	}

	if cfg.Delay > 0 {
		builder = builder.WithDelay(cfg.Delay)
	}

	if cfg.OnStateChanged != nil {
		builder = builder.OnStateChanged(cfg.OnStateChanged)
	}

	return builder.Build()
}

// NewBulkhead 构建并发隔离舱，限制对下游的最大并发请求数并提供排队等待缓冲。
func NewBulkhead[R any](cfg BulkheadConfig) bulkhead.Bulkhead[R] {
	builder := bulkhead.NewBuilder[R](cmp.Or(cfg.MaxConcurrency, 10))
	if cfg.MaxWaitTime > 0 {
		builder = builder.WithMaxWaitTime(cfg.MaxWaitTime)
	}
	return builder.Build()
}

// NewTimeout 构建操作级超时控制策略。
func NewTimeout[R any](dur time.Duration) timeout.Timeout[R] {
	return timeout.New[R](dur)
}

// NewRateLimiter 构建面向出站调用的流量整形与限速策略。
func NewRateLimiter[R any](cfg RateLimiterConfig) ratelimiter.RateLimiter[R] {
	period := cmp.Or(cfg.Period, time.Second)
	rate := cmp.Or(cfg.MaxRate, 100)

	builder := lo.Ternary(
		cfg.Smooth,
		ratelimiter.NewSmoothBuilder[R](rate, period),
		ratelimiter.NewBurstyBuilder[R](rate, period),
	)

	if cfg.MaxWaitTime > 0 {
		builder = builder.WithMaxWaitTime(cfg.MaxWaitTime)
	}

	return builder.Build()
}

// NewFallbackWithResult 构建返回固定保底结果的降级策略。
func NewFallbackWithResult[R any](result R) fallback.Fallback[R] {
	return fallback.NewWithResult(result)
}

// NewFallbackWithFunc 构建基于动态回调函数的降级策略。
func NewFallbackWithFunc[R any](fn func(failsafe.Execution[R]) (R, error)) fallback.Fallback[R] {
	return fallback.NewWithFunc(fn)
}

// NewHedgePolicy 构建用于消除 P99 长尾延迟的对冲请求策略。
func NewHedgePolicy[R any](cfg HedgePolicyConfig) hedgepolicy.HedgePolicy[R] {
	builder := hedgepolicy.NewBuilderWithDelay[R](cmp.Or(cfg.Delay, 50*time.Millisecond))
	if cfg.MaxHedges > 0 {
		builder = builder.WithMaxHedges(cfg.MaxHedges)
	}
	if cfg.Budget != nil {
		builder = builder.WithBudget(cfg.Budget)
	}
	return builder.Build()
}

// NewBudget 构建全局重试/对冲预算，防止下游服务故障时引发重试雪崩风暴。
func NewBudget(cfg BudgetConfig) budget.Budget {
	builder := budget.NewBuilder()
	if cfg.MaxRate > 0 {
		builder = builder.WithMaxRate(cfg.MaxRate)
	}
	if cfg.MinConcurrency > 0 {
		builder = builder.WithMinConcurrency(cfg.MinConcurrency)
	}
	return builder.Build()
}

// NewHTTPRoundTripper 将弹性策略挂载至原生 http.RoundTripper 传输层，供标准 http.Client 或 resty.Client 使用。
func NewHTTPRoundTripper(inner http.RoundTripper, policies ...failsafe.Policy[*http.Response]) http.RoundTripper {
	return failsafehttp.NewRoundTripper(cmp.Or(inner, http.DefaultTransport), policies...)
}

// NewExecutor 根据给定的弹性策略列表构建执行器。
//
// 策略组合顺序遵循由外向内（从左到右，Outer-to-Inner）。
// 例如 NewExecutor(overallTimeout, retryPolicy, attemptTimeout) 中：
// overallTimeout 覆盖全局总耗时，retryPolicy 管理重试，attemptTimeout 仅约束单次重试尝试。
func NewExecutor[R any](policies ...failsafe.Policy[R]) failsafe.Executor[R] {
	return failsafe.With(policies...)
}

// NewNetworkExecutor 构建一套面向标准出站网络调用的预设执行器（包含重试策略）。
func NewNetworkExecutor(maxAttempts int, initialDelay, maxDelay time.Duration) failsafe.Executor[any] {
	retry := NewRetryPolicy[any](RetryPolicyConfig{
		MaxAttempts: maxAttempts,
		Delay:       initialDelay,
		MaxDelay:    maxDelay,
	})
	return failsafe.With(retry)
}

// Run 遵循上下文并在执行器保护下运行无返回值的任务。
func Run[R any](ctx context.Context, executor failsafe.Executor[R], fn func() error) error {
	ctx = cmp.Or(ctx, context.Background())
	if executor == nil {
		return fn()
	}
	return executor.WithContext(ctx).Run(fn)
}

// RunWithContext 遵循上下文并在执行器保护下运行接收 context 的任务。
// 闭包所接收的 Context 自动集成执行器层级的超时或对冲派生控制。
func RunWithContext[R any](ctx context.Context, executor failsafe.Executor[R], fn func(context.Context) error) error {
	ctx = cmp.Or(ctx, context.Background())
	if executor == nil {
		return fn(ctx)
	}
	return executor.WithContext(ctx).RunWithExecution(func(exec failsafe.Execution[R]) error {
		return fn(exec.Context())
	})
}

// Get 遵循上下文并在执行器保护下运行有返回值的任务。
func Get[T any](ctx context.Context, executor failsafe.Executor[T], fn func() (T, error)) (T, error) {
	ctx = cmp.Or(ctx, context.Background())
	if executor == nil {
		return fn()
	}
	return executor.WithContext(ctx).Get(fn)
}

// GetWithContext 遵循上下文并在执行器保护下运行接收 context 的任务并返回值。
// 闭包所接收的 Context 自动集成执行器层级的超时或对冲派生控制。
func GetWithContext[T any](ctx context.Context, executor failsafe.Executor[T], fn func(context.Context) (T, error)) (T, error) {
	ctx = cmp.Or(ctx, context.Background())
	if executor == nil {
		return fn(ctx)
	}
	return executor.WithContext(ctx).GetWithExecution(func(exec failsafe.Execution[T]) (T, error) {
		return fn(exec.Context())
	})
}
