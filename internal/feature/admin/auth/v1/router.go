package v1

import (
	"github.com/gin-gonic/gin"

	"cgin/internal/middleware"
)

// RegisterRoutes 注册后台管理员认证端点（/v1/admin/auth/*）。
func RegisterRoutes(router *gin.RouterGroup, h *Handler, mw *middleware.Middleware) {
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

// RegisterAdminAuthRoutes 兼容命名别名。
var RegisterAdminAuthRoutes = RegisterRoutes
