package routes

import (
	"github.com/gin-gonic/gin"

	"cgin/internal/handler"
	"cgin/internal/middleware"
)

// RegisterPaymentRoutes 在 /v1/payments 下注册支付网关公开回调端点。
// 渠道异步通知与同步跳转均为公开外部端点，绝不挂载 JWT 认证中间件。
func RegisterPaymentRoutes(router *gin.RouterGroup, h *handler.PaymentHandler, _ *middleware.Middleware) {
	group := router.Group("/payments")

	// 异步回调通知端点：支持默认应用与多租户/多应用
	group.POST("/notify/:channel", h.HandleNotification)
	group.POST("/notify/:channel/:app", h.HandleNotification)

	// 支付宝前端同步重定向端点
	group.GET("/return/alipay", h.HandleAlipayReturn)
}
