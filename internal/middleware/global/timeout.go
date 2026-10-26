package global

import (
	"context"
	"strings"

	"github.com/gin-contrib/timeout"
	"github.com/gin-gonic/gin"

	"cgin/pkg/errs"
)

// Timeout 基于 gin-contrib/timeout 控制请求超时，超时输出 504，长连接自动豁免。
func (m *Middleware) Timeout() gin.HandlerFunc {
	if m.requestTimeout <= 0 {
		return func(c *gin.Context) {
			c.Next()
		}
	}

	base := timeout.New(
		timeout.WithTimeout(m.requestTimeout),
		timeout.WithResponse(func(c *gin.Context) {
			errs.Render(c, errs.GatewayTimeout(context.DeadlineExceeded))
		}),
	)

	return func(c *gin.Context) {
		// 对 WebSocket 升级握手及 SSE 流式长连接请求进行超时豁免
		if isStreamingRequest(c) {
			c.Next()
			return
		}

		base(c)
	}
}

// isStreamingRequest 判断是否为 WebSocket 握手或 SSE 长连接，这类请求豁免超时。
func isStreamingRequest(c *gin.Context) bool {
	return strings.EqualFold(c.GetHeader("Upgrade"), "websocket") ||
		strings.Contains(c.GetHeader("Accept"), "text/event-stream")
}
