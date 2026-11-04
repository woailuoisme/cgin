package global

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// faviconPath 是浏览器自动探测的站点图标路径。
const faviconPath = "/favicon.ico"

// Favicon 拦截浏览器和爬虫静默 /favicon.ico 请求，避免产生 404 噪音与路由开销。
// 对齐 Fiber favicon.New() 默认语义（gin-contrib 无官方对应实现，属项目自建）。
func (m *Middleware) Favicon() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.URL.Path != faviconPath {
			c.Next()
			return
		}

		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Header("Allow", "GET, HEAD, OPTIONS")
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusOK)
				return
			}
			c.AbortWithStatus(http.StatusMethodNotAllowed)
			return
		}

		c.AbortWithStatus(http.StatusNoContent)
	}
}
