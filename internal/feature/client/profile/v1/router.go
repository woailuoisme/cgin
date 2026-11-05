package v1

import (
	"github.com/gin-gonic/gin"

	"cgin/internal/middleware"
)

// RegisterRoutes 注册前端用户个人档案端点 (/v1/users)。
func RegisterRoutes(router *gin.RouterGroup, h *Handler, mw *middleware.Middleware) {
	group := router.Group("/users", mw.Auth("client"), mw.Throttle(middleware.TierAuthenticated))

	group.GET("/me", h.GetProfile)
	group.PATCH("/me", h.UpdateProfile)
	group.POST("/me/avatar", h.UploadAvatar)
}

// RegisterProfileRoutes 兼容别名。
func RegisterProfileRoutes(router *gin.RouterGroup, h *Handler, mw *middleware.Middleware) {
	RegisterRoutes(router, h, mw)
}
