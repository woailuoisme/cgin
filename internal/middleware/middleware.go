// Package middleware 提供业务路由/路由组级 HTTP 中间件。
//
// 包含按需挂载于特定业务路由或 Group 的端点保护与控制中间件：
//   - Throttle: 类似 Laravel 的平滑滑动窗口限流（支持字符串表达式 "60,1"、命名限流器、预设档位 Tier 及强类型参数）
//   - Auth: JWT 认证守卫（支持 "client"、"admin" 等多守卫策略与多 Guard 顺序回退）
//   - Can: 基于 Casbin RBAC 的细粒度资源与操作权限校验
//
// 全局传输层中间件请参见子包 bgin/internal/middleware/global。
package middleware

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/authentication"
	"bgin/internal/platform/authorization"
	"bgin/internal/platform/cache"
)

// Deps 是路由/组中间件所需的依赖，由 bootstrap 装配后注入。
type Deps struct {
	Tokens      *authentication.TokenIssuer
	AuthManager *authentication.Manager
	Cache       cache.Store
	Authz       *authorization.Enforcer
	Logger      *zap.Logger
	Limiter     config.LimiterConfig
	SigningKey  string
}

// Middleware 持有路由级中间件所需的依赖。
type Middleware struct {
	tokens       *authentication.TokenIssuer
	authManager  *authentication.Manager
	cache        cache.Store
	limiterStore RateLimiterStore
	authz        *authorization.Enforcer
	logger       *zap.Logger
	limiter      config.LimiterConfig
	signingKey   string
}

// New 构造路由中间件实例。
func New(deps Deps) *Middleware {
	return &Middleware{
		tokens:      deps.Tokens,
		authManager: deps.AuthManager,
		cache:       deps.Cache,
		authz:       deps.Authz,
		logger:      deps.Logger,
		limiter:     deps.Limiter,
		signingKey:  deps.SigningKey,
	}
}

// SetLimiterStore 替换限流器底层存储实现（主要用于单元测试与自定义存储注入）。
func (m *Middleware) SetLimiterStore(store RateLimiterStore) {
	m.limiterStore = store
}

// abortWithError 向上下文附加错误并立即中止后续处理。
func abortWithError(c *gin.Context, err error) {
	_ = c.Error(err)
	c.Abort()
}
