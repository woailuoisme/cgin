// Package routes 集中登记客户端 HTTP 端点：只做路由分组与中间件编排，不含业务逻辑。
package routes

import (
	"github.com/gin-gonic/gin"

	"cgin/internal/handler"
	"cgin/internal/middleware"
)

// RegisterClientAuthRoutes 在 /v1/auth 下注册全部认证端点。
// 敏感端点走最严配额，需鉴权的端点走已认证配额，两者互不叠加。
func RegisterClientAuthRoutes(router *gin.RouterGroup, h *handler.ClientAuthHandler, mw *middleware.Middleware) {
	group := router.Group("/auth")

	// 敏感端点：通过语义化子路由统一级联敏感配额限流中间件。
	sensitive := group.Group("", mw.Throttle(middleware.TierSensitive))
	sensitive.POST("/sign-up/email", h.SignUpEmail)
	sensitive.POST("/sign-in/email", h.SignInEmail)
	sensitive.POST("/sign-in/username", h.SignInUsername)
	sensitive.POST("/token/refresh", h.Refresh)
	sensitive.POST("/send-verification-otp", h.SendVerificationOTP)
	sensitive.POST("/verify-email", h.VerifyEmail)
	sensitive.POST("/forget-password", h.ForgetPassword)
	sensitive.POST("/request-password-reset", h.RequestPasswordReset)
	sensitive.POST("/reset-password", h.ResetPassword)

	// 需鉴权端点：通过语义化子路由统一级联已认证配额与身份校验中间件。
	protected := group.Group("", mw.Auth("client"), mw.Throttle(middleware.TierAuthenticated))
	protected.GET("/get-session", h.GetSession)
	protected.POST("/sign-out", h.SignOut)
	protected.POST("/change-password", h.ChangePassword)
}
