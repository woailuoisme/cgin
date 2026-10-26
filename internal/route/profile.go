package route

import (
	"github.com/gin-gonic/gin"

	"cgin/internal/handler"
	"cgin/internal/middleware"
)

// RegisterProfileRoutes 注册前端用户个人档案端点 (/v1/users)。
func RegisterProfileRoutes(router *gin.RouterGroup, h *handler.ProfileHandler, mw *middleware.Middleware) {
	group := router.Group("/users", mw.Auth("client"), mw.Throttle(middleware.TierAuthenticated))

	group.GET("/me", h.GetProfile)
	group.PATCH("/me", h.UpdateProfile)
	group.POST("/me/avatar", h.UploadAvatar)
}
