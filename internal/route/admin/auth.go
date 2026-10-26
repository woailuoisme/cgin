// Package adminroute 注册后台管理端路由，与顶层 route 包按作用域隔离。
package adminroute

import (
	"github.com/gin-gonic/gin"

	adminhandler "cgin/internal/handler/admin"
	"cgin/internal/middleware"
)

// RegisterAdminAuthRoutes 注册后台管理员认证端点（/v1/admin/auth/*）。
func RegisterAdminAuthRoutes(router *gin.RouterGroup, h *adminhandler.AuthHandler, mw *middleware.Middleware) {
	group := router.Group("/admin/auth")

	// 敏感公开端点（敏感限流防刷）
	sensitive := group.Group("", mw.Throttle(middleware.TierSensitive))
	sensitive.POST("/sign-in/email", h.SignInEmail)
	sensitive.POST("/token/refresh", h.Refresh)

	// 受保护会话端点（管理员守卫鉴权，无需额外 Casbin 业务权限码）
	protected := group.Group("", mw.Auth("admin"), mw.Throttle(middleware.TierAuthenticated))
	protected.GET("/get-session", h.GetSession)
	protected.POST("/sign-out", h.SignOut)
	protected.POST("/change-password", h.ChangePassword)
}
