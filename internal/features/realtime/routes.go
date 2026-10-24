package realtime

import (
	"github.com/gin-gonic/gin"

	"bgin/internal/middleware"
)

// RegisterRoutes 注册实时通信客户端端点与 Centrifugo 代理回调端点。
func RegisterRoutes(router *gin.RouterGroup, handler *Handler, mw *middleware.Middleware) {
	group := router.Group("/realtime")

	// 客户端连接凭据（需要客户端守卫身份认证，速率限制为 authenticated）
	group.GET("/token", mw.Auth("client"), mw.Throttle(middleware.TierAuthenticated), handler.GetToken)

	// Centrifugo 反向代理鉴权端点（基于 HMAC-SHA256 签名校验，不套用用户层 IP 限流）。
	group.POST("/proxy/subscribe", handler.ProxySubscribe)
}
