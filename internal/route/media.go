package route

import (
	"github.com/gin-gonic/gin"

	"cgin/internal/handler"
	"cgin/internal/middleware"
)

// RegisterMediaRoutes 注册媒体相关端点 (/v1/media)。
func RegisterMediaRoutes(router *gin.RouterGroup, h *handler.MediaHandler, mw *middleware.Middleware) {
	group := router.Group("/media", mw.Auth("client"), mw.Throttle(middleware.TierAuthenticated))

	group.POST("/presign", h.Presign)
}
