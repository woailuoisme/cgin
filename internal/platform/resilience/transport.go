// Package resilience 封装通用的弹性容错机制。
package resilience

import (
	"cmp"
	"net/http"
	"strconv"
	"time"

	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/budget"
	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/failsafe-go/failsafe-go/failsafehttp"
)

// ResilientTransportConfig 弹性 HTTP 传输层配置项。
type ResilientTransportConfig struct {
	BaseTransport  http.RoundTripper     // 底层 RoundTripper，缺省为 http.DefaultTransport
	MaxAttempts    int                   // 最大尝试次数（<=0 兜底为 3）
	InitialDelay   time.Duration         // 初始退避延迟（<=0 兜底为 100ms）
	MaxDelay       time.Duration         // 最大退避延迟（<=0 兜底为 2s）
	Timeout        time.Duration         // 客户端总体超时时间（若 >0 则设置到 http.Client）
	CircuitBreaker *CircuitBreakerConfig // 可选熔断断路器配置
	Bulkhead       *BulkheadConfig       // 可选并发隔离舱配置
	RetryBudget    budget.Budget         // 可选重试预算
}

// NewResilientTransport 根据配置构造集成了重试、熔断断路器、隔离舱的弹性 http.RoundTripper。
func NewResilientTransport(inner http.RoundTripper, cfg ResilientTransportConfig) http.RoundTripper {
	inner = cmp.Or(inner, cfg.BaseTransport, http.DefaultTransport)

	maxAttempts := cmp.Or(cfg.MaxAttempts, 3)
	initialDelay := cmp.Or(cfg.InitialDelay, 100*time.Millisecond)
	maxDelay := cmp.Or(cfg.MaxDelay, 2*time.Second)

	retryBuilder := failsafehttp.NewRetryPolicyBuilder().
		WithMaxAttempts(maxAttempts).
		WithBackoff(initialDelay, maxDelay).
		AbortIf(func(resp *http.Response, _ error) bool {
			if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
				if d := parseRetryAfter(resp); d > maxDelay {
					return true
				}
			}
			return false
		})

	if cfg.RetryBudget != nil {
		retryBuilder = retryBuilder.WithBudget(cfg.RetryBudget)
	}

	policies := make([]failsafe.Policy[*http.Response], 0, 4)

	// 1. Bulkhead 并发隔离（若配置）
	if cfg.Bulkhead != nil {
		policies = append(policies, NewBulkhead[*http.Response](*cfg.Bulkhead))
	}

	// 2. RetryPolicy 重试策略（支持 429 Retry-After, 5xx 与底层网络异常）
	policies = append(policies, retryBuilder.Build())

	// 3. CircuitBreaker 熔断保护（若配置，将下游 5xx 与网络故障计入断路器失败率）
	if cfg.CircuitBreaker != nil {
		policies = append(policies, buildHTTPCircuitBreaker(*cfg.CircuitBreaker))
	}

	return NewHTTPRoundTripper(inner, policies...)
}

func parseRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	val := resp.Header.Get("Retry-After")
	if val == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(val); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if targetTime, err := http.ParseTime(val); err == nil {
		if delay := time.Until(targetTime); delay > 0 {
			return delay
		}
	}
	return 0
}

// buildHTTPCircuitBreaker 构造针对出站 HTTP 调用的断路器策略。
func buildHTTPCircuitBreaker(cfg CircuitBreakerConfig) failsafe.Policy[*http.Response] {
	builder := circuitbreaker.NewBuilder[*http.Response]().
		HandleIf(func(resp *http.Response, err error) bool {
			return err != nil || (resp != nil && resp.StatusCode >= 500)
		})
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

// NewResilientHTTPClient 构造基于弹性传输层的标准 *http.Client。
func NewResilientHTTPClient(cfg ResilientTransportConfig) *http.Client {
	transport := NewResilientTransport(cfg.BaseTransport, cfg)
	client := &http.Client{
		Transport: transport,
	}
	if cfg.Timeout > 0 {
		client.Timeout = cfg.Timeout
	}
	return client
}
