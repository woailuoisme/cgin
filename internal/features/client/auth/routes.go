package auth

import (
	"github.com/gin-gonic/gin"

	"bgin/internal/middleware"
)

// RegisterRoutes 在 /v1/auth 下注册全部认证端点。
// 敏感端点走最严配额，需鉴权的端点走已认证配额，两者互不叠加。
func RegisterRoutes(router *gin.RouterGroup, handler *Handler, mw *middleware.Middleware) {
	group := router.Group("/auth")

	// 敏感端点：通过语义化子路由统一级联敏感配额限流中间件。
	sensitive := group.Group("", mw.Throttle(middleware.TierSensitive))
	sensitive.POST("/sign-up/email", handler.SignUpEmail)
	sensitive.POST("/sign-in/email", handler.SignInEmail)
	sensitive.POST("/sign-in/username", handler.SignInUsername)
	sensitive.POST("/token/refresh", handler.Refresh)
	sensitive.POST("/send-verification-otp", handler.SendVerificationOTP)
	sensitive.POST("/verify-email", handler.VerifyEmail)
	sensitive.POST("/forget-password", handler.ForgetPassword)
	sensitive.POST("/request-password-reset", handler.RequestPasswordReset)
	sensitive.POST("/reset-password", handler.ResetPassword)

	// 需鉴权端点：通过语义化子路由统一级联已认证配额与身份校验中间件。
	protected := group.Group("", mw.Auth("client"), mw.Throttle(middleware.TierAuthenticated))
	protected.GET("/get-session", handler.GetSession)
	protected.POST("/sign-out", handler.SignOut)
	protected.POST("/change-password", handler.ChangePassword)
}
