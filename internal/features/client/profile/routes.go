// Package profile 实现前端用户个人档案模块 (/v1/users/me)。
package profile

import (
	"github.com/gin-gonic/gin"

	"bgin/internal/middleware"
)

// RegisterRoutes 注册前端用户个人档案端点 (/v1/users)。
func RegisterRoutes(router *gin.RouterGroup, handler *Handler, mw *middleware.Middleware) {
	group := router.Group("/users", mw.Auth("client"), mw.Throttle(middleware.TierAuthenticated))

	group.GET("/me", handler.GetProfile)
	group.PATCH("/me", handler.UpdateProfile)
	group.POST("/me/avatar", handler.UploadAvatar)
}
