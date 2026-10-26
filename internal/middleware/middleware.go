// Package middleware 提供按路由/路由组挂载的业务中间件：
// Throttle（滑动窗口限流）、Auth（JWT 守卫，支持多守卫顺序回退）、Can（Casbin 资源级鉴权）。
//
// 全局传输层中间件见子包 cgin/internal/middleware/global。
package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"cgin/internal/config"
	"cgin/pkg/authentication"
	"cgin/pkg/authorization"
	"cgin/pkg/cache"
)

// Deps 是路由/组中间件所需的依赖，由 bootstrap 装配后注入。
type Deps struct {
	AuthManager *authentication.Manager
	Cache       cache.Store
	Authz       *authorization.Enforcer
	Logger      *zerolog.Logger
	Limiter     config.LimiterConfig
	SigningKey  string
}

// Middleware 持有路由级中间件所需的依赖。
type Middleware struct {
	authManager  *authentication.Manager
	cache        cache.Store
	limiterStore RateLimiterStore
	authz        *authorization.Enforcer
	logger       *zerolog.Logger
	limiter      config.LimiterConfig
	signingKey   string
}

// New 构造路由中间件实例。
func New(deps Deps) *Middleware {
	return &Middleware{
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
