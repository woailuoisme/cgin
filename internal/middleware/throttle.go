package middleware

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"cgin/internal/platform/authentication"
	"cgin/internal/platform/cache"
	"cgin/internal/platform/errs"
	"cgin/internal/platform/logger"
)

// Tier 定义业务限流预设档位。
type Tier string

const (
	// TierAnonymous 面向未认证客户端，按客户端 IP 计数。
	TierAnonymous Tier = "anonymous"
	// TierAuthenticated 面向已认证用户，按用户 ID 计数。
	TierAuthenticated Tier = "authenticated"
	// TierSensitive 面向敏感认证端点，按客户端 IP 计数且配额最严。
	TierSensitive Tier = "sensitive"

	rateLimitWindow = time.Minute
)

// ThrottleOption 定义自定义限流选项。
type ThrottleOption func(*throttleOptions)

type throttleOptions struct {
	keyFunc  func(c *gin.Context) string
	tier     Tier
	response func(c *gin.Context)
}

// WithThrottleKey 自定义限流区分维度（例如按指定 header 或复合 key）。
func WithThrottleKey(fn func(c *gin.Context) string) ThrottleOption {
	return func(o *throttleOptions) {
		o.keyFunc = fn
	}
}

// WithThrottleTier 指定所属业务档位（用于监控与日志分类）。
func WithThrottleTier(tier Tier) ThrottleOption {
	return func(o *throttleOptions) {
		o.tier = tier
	}
}

// WithThrottleResponse 指定自定义限流触发时的响应拦截回调。
func WithThrottleResponse(fn func(c *gin.Context)) ThrottleOption {
	return func(o *throttleOptions) {
		o.response = fn
	}
}

// RateLimiterStore 定义限流中间件所需的缓存原语接口。
// platform/cache.Client 结构体天然完整实现该接口。
type RateLimiterStore interface {
	Increment(ctx context.Context, key string, ttl time.Duration) (int64, error)
	Get(ctx context.Context, key string) (string, error)
}

// Throttle 【路由/路由组中间件】按业务档位预设配额执行平滑滑动窗口限流（Sliding Window Counter）。
// 配额自动从系统配置中读取（Anonymous, Authenticated, Sensitive）。
func (m *Middleware) Throttle(tier Tier, opts ...ThrottleOption) gin.HandlerFunc {
	if m != nil && !m.limiter.IsEnabled() {
		return func(c *gin.Context) { c.Next() }
	}
	allOpts := make([]ThrottleOption, 0, len(opts)+1)
	allOpts = append(allOpts, WithThrottleTier(tier))
	allOpts = append(allOpts, opts...)
	return ThrottleWithStore(m.resolveLimiterStore(), m.limitFor(tier), rateLimitWindow, allOpts...)
}

// ThrottleRate 【路由/路由组中间件】按指定的具体配额与时间周期执行平滑滑动窗口限流。
func (m *Middleware) ThrottleRate(limit int, window time.Duration, opts ...ThrottleOption) gin.HandlerFunc {
	if m != nil && !m.limiter.IsEnabled() {
		return func(c *gin.Context) { c.Next() }
	}
	return ThrottleWithStore(m.resolveLimiterStore(), limit, window, opts...)
}

func (m *Middleware) resolveLimiterStore() RateLimiterStore {
	if m == nil {
		return nil
	}
	if m.limiterStore != nil {
		return m.limiterStore
	}
	if m.cache != nil {
		return m.cache
	}
	return nil
}

// ThrottleWithStore 构造独立的平滑滑动窗口限流中间件实例，方便测试与自定义注入。
func ThrottleWithStore(store RateLimiterStore, limit int, window time.Duration, opts ...ThrottleOption) gin.HandlerFunc {
	var options throttleOptions
	for _, opt := range opts {
		opt(&options)
	}
	if window <= 0 {
		window = rateLimitWindow
	}

	return func(c *gin.Context) {
		if limit <= 0 || store == nil {
			c.Next()
			return
		}

		now := time.Now().UTC()
		windowSeconds := max(int64(window.Seconds()), 1)
		bucket := now.Unix() / windowSeconds
		offset := now.Unix() % windowSeconds
		resetAt := (bucket + 1) * windowSeconds

		scope := resolveThrottleScope(c, options)
		tierName := throttleTierLabel(options.tier)

		key := cache.Key("ratelimit", tierName, scope, strconv.FormatInt(windowSeconds, 10), strconv.FormatInt(bucket, 10))
		currCount, err := store.Increment(c.Request.Context(), key, 2*window)
		if err != nil {
			// 计数器故障时降级放行并记录错误。
			logger.FromContext(c.Request.Context()).Error("throttle counter failed, request allowed", zap.Error(err))
			c.Next()
			return
		}

		// 平滑滑动窗口算法 (Sliding Window Counter)：结合前一窗口的加权余量，平滑消除窗口临界双倍突发流量。
		prevKey := cache.Key("ratelimit", tierName, scope, strconv.FormatInt(windowSeconds, 10), strconv.FormatInt(bucket-1, 10))
		prevCount := previousWindowCount(c, store, prevKey)
		weight := float64(windowSeconds-offset) / float64(windowSeconds)
		estimatedCount := currCount + int64(float64(prevCount)*weight)

		if estimatedCount > int64(limit) {
			writeExceededHeaders(c, limit, resetAt, now.Unix())
			logRateLimitExceeded(c, tierName, scope, limit, windowSeconds, currCount, estimatedCount)

			if options.response != nil {
				options.response(c)
			} else {
				abortWithError(c, errs.RateLimited("Too many requests"))
			}
			c.Abort()
			return
		}

		writeLimitHeaders(c, limit, int64(limit)-estimatedCount, resetAt)
		c.Next()
	}
}

// resolveThrottleScope 解析限流区分维度：自定义键优先，其次业务档位，最后默认维度。
func resolveThrottleScope(c *gin.Context, options throttleOptions) string {
	switch {
	case options.keyFunc != nil:
		return options.keyFunc(c)
	case options.tier != "":
		return scopeKey(options.tier, c)
	default:
		return defaultScopeKey(c)
	}
}

// throttleTierLabel 返回日志用的档位名称，自定义限流记为 custom。
func throttleTierLabel(tier Tier) string {
	if tier == "" {
		return "custom"
	}
	return string(tier)
}

// previousWindowCount 读取前一窗口的计数；缺失或解析失败时按 0 处理。
func previousWindowCount(c *gin.Context, store RateLimiterStore, key string) int64 {
	value, err := store.Get(c.Request.Context(), key)
	if err != nil {
		return 0
	}
	count, _ := strconv.ParseInt(value, 10, 64)
	return count
}

// writeLimitHeaders 写入未超限时的配额响应头。
func writeLimitHeaders(c *gin.Context, limit int, remaining, resetAt int64) {
	c.Header("X-RateLimit-Limit", strconv.Itoa(limit))
	c.Header("X-RateLimit-Remaining", strconv.FormatInt(max(remaining, 0), 10))
	c.Header("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
}

// writeExceededHeaders 写入超限时的配额与重试响应头。
func writeExceededHeaders(c *gin.Context, limit int, resetAt, now int64) {
	writeLimitHeaders(c, limit, 0, resetAt)
	c.Header("Retry-After", strconv.FormatInt(max(resetAt-now, 1), 10))
}

// logRateLimitExceeded 记录限流拒绝的结构化审计日志。
func logRateLimitExceeded(c *gin.Context, tier, scope string, limit int, windowSeconds, currentCount, estimatedCount int64) {
	logger.FromContext(c.Request.Context()).Warn(
		"rate limit exceeded",
		zap.String("tier", tier),
		zap.String("scope", scope),
		zap.Int("limit", limit),
		zap.Int64("window_seconds", windowSeconds),
		zap.Int64("current_count", currentCount),
		zap.Int64("estimated_count", estimatedCount),
		zap.String("path", c.Request.URL.Path),
		zap.String("method", c.Request.Method),
		zap.String("ip", c.ClientIP()),
	)
}

// limitFor 按档位读取配额，未知档位回退至未认证配额。
func (m *Middleware) limitFor(tier Tier) int {
	if m != nil && !m.limiter.IsEnabled() {
		return 0
	}
	switch tier {
	case TierAuthenticated:
		return m.limiter.Authenticated
	case TierSensitive:
		return m.limiter.Sensitive
	default:
		return m.limiter.Anonymous
	}
}

// defaultScopeKey 默认限流维度：优先使用已认证用户 ID，未认证时回退为客户端 IP。
func defaultScopeKey(c *gin.Context) string {
	if id, ok := authentication.FromContext(c.Request.Context()); ok && id.UserID != "" {
		return "user:" + id.UserID
	}
	return "ip:" + c.ClientIP()
}

// scopeKey 确定特定档位的限流维度：TierAuthenticated 优先使用 UserID，其余均使用客户端 IP。
func scopeKey(tier Tier, c *gin.Context) string {
	if tier == TierAuthenticated {
		return defaultScopeKey(c)
	}
	return "ip:" + c.ClientIP()
}
