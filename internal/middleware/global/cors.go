package global

import (
	"slices"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// CORS 【全局中间件】基于 gin-contrib/cors 统一处理跨域资源共享与 OPTIONS 预检请求。
func (m *Middleware) CORS() gin.HandlerFunc {
	cfg := cors.Config{
		AllowMethods: []string{
			"GET", "POST", "PATCH", "PUT", "DELETE", "HEAD", "OPTIONS",
		},
		AllowHeaders: []string{
			"Authorization", "Content-Type", "Accept", "x-request-id", "Idempotency-Key", "Accept-Language",
		},
		ExposeHeaders: []string{
			"x-request-id", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After",
		},
		MaxAge: 12 * time.Hour,
	}

	if slices.Contains(m.cors.AllowedOrigins, "*") {
		cfg.AllowAllOrigins = true
		cfg.AllowCredentials = false // 遵循 W3C 规范：通配符禁止回显允许凭证
	} else {
		cfg.AllowOrigins = m.cors.AllowedOrigins
		cfg.AllowCredentials = true
	}

	return cors.New(cfg)
}
