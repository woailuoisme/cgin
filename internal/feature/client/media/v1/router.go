package v1

import (
	"github.com/gin-gonic/gin"

	"cgin/internal/middleware"
)

// RegisterRoutes 注册媒体相关端点 (/v1/media)。
func RegisterRoutes(router *gin.RouterGroup, h *Handler, mw *middleware.Middleware) {
	group := router.Group("/media", mw.Auth("client"), mw.Throttle(middleware.TierAuthenticated))

	group.POST("/presign", h.Presign)
}

// RegisterMediaRoutes 兼容别名。
func RegisterMediaRoutes(router *gin.RouterGroup, h *Handler, mw *middleware.Middleware) {
	RegisterRoutes(router, h, mw)
}
