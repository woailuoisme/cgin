package adminauth

import (
	"github.com/gin-gonic/gin"

	"bgin/internal/middleware"
)

// RegisterAdminAuthRoutes 注册后台管理员认证端点（/v1/admin/auth/*）。
func RegisterAdminAuthRoutes(router *gin.RouterGroup, handler *Handler, mw *middleware.Middleware) {
	group := router.Group("/admin/auth")

	// 敏感公开端点（敏感限流防刷）
	sensitive := group.Group("", mw.Throttle(middleware.TierSensitive))
	sensitive.POST("/sign-in/email", handler.SignInEmail)
	sensitive.POST("/token/refresh", handler.Refresh)

	// 受保护会话端点（管理员守卫鉴权，无需额外 Casbin 业务权限码）
	protected := group.Group("", mw.Auth("admin"), mw.Throttle(middleware.TierAuthenticated))
	protected.GET("/get-session", handler.GetSession)
	protected.POST("/sign-out", handler.SignOut)
	protected.POST("/change-password", handler.ChangePassword)
}
