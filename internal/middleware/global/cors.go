package global

import (
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	// allowedRequestHeaders 跨域预检允许携带的请求头。
	allowedRequestHeaders = "Authorization, Content-Type, Accept, x-request-id, Idempotency-Key"
	// exposedResponseHeaders 允许浏览器脚本读取的响应头。
	exposedResponseHeaders = "x-request-id, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, Retry-After"
)

// CORS 【全局中间件】处理跨域预检与响应头配置。
func (m *Middleware) CORS() gin.HandlerFunc {
	allowAll := slices.Contains(m.cors.AllowedOrigins, "*")
	allowed := make(map[string]struct{}, len(m.cors.AllowedOrigins))
	for _, origin := range m.cors.AllowedOrigins {
		if origin != "*" {
			allowed[strings.ToLower(origin)] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}

		_, listed := allowed[strings.ToLower(origin)]
		switch {
		case listed:
			// 白名单来源：回显具体来源、允许凭证并追加 Vary 头。
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Writer.Header().Add("Vary", "Origin")
		case allowAll:
			// 通配符来源：仅回显通配符，规范禁止携带凭证。
			c.Header("Access-Control-Allow-Origin", "*")
		}

		if listed || allowAll {
			c.Header("Access-Control-Allow-Headers", allowedRequestHeaders)
			c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Expose-Headers", exposedResponseHeaders)
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
