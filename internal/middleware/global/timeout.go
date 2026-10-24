package global

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"bgin/internal/platform/errs"
)

// Timeout 【全局中间件】向请求上下文注入超时截止期，超时由下层 I/O 响应并经 ErrorHandler 统一渲染为 504 错误。
//
// 自动对 WebSocket 升级握手及 SSE (text/event-stream) 流式长连接进行超时豁免。
func (m *Middleware) Timeout() gin.HandlerFunc {
	return func(c *gin.Context) {
		if m.requestTimeout <= 0 || isStreamingRequest(c) {
			c.Next()
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), m.requestTimeout)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)
		c.Next()

		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return
		}
		if !c.Writer.Written() {
			if len(c.Errors) == 0 || !errors.Is(c.Errors.Last().Err, context.DeadlineExceeded) {
				_ = c.Error(errs.GatewayTimeout(context.DeadlineExceeded))
			}
			c.Abort()
			return
		}
		if m.logger != nil {
			m.logger.Warn("request timed out after partial response",
				zap.String("path", c.Request.URL.Path),
				zap.String("method", c.Request.Method),
			)
		}
	}
}

// isStreamingRequest 检测是否为 WebSocket 握手或 SSE (Server-Sent Events) 流式长连接请求。
func isStreamingRequest(c *gin.Context) bool {
	if strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
		return true
	}
	if strings.Contains(c.GetHeader("Accept"), "text/event-stream") {
		return true
	}
	return false
}
