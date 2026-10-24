package global

import (
	"strings"
	"time"

	sentrygin "github.com/getsentry/sentry-go/gin"
	"github.com/gin-gonic/gin"

	platformsentry "bgin/internal/platform/sentry"
)

// Sentry 【全局中间件】挂载 Sentry / GlitchTip 请求作用域 Hub、关联 request_id 与 HTTP 事务。
func (m *Middleware) Sentry() gin.HandlerFunc {
	if !platformsentry.IsInitialized() {
		return func(c *gin.Context) {
			c.Next()
		}
	}

	handler := sentrygin.New(sentrygin.Options{
		Repanic:         true,
		WaitForDelivery: false,
		Timeout:         2 * time.Second,
	})

	return func(c *gin.Context) {
		// 对高频系统探针路由忽略 Sentry Hub 装配与事务采样，降低打点开销与内存分配
		if isProbePath(c.Request.URL.Path) || isProbePath(c.FullPath()) {
			c.Next()
			return
		}

		handler(c)

		// 为请求作用域的 Sentry Hub 注入链路追踪与上下文标签
		if hub := sentrygin.GetHubFromContext(c); hub != nil {
			reqID := strings.TrimSpace(c.Writer.Header().Get(headerRequestID))
			if reqID == "" {
				reqID = strings.TrimSpace(c.GetHeader(headerRequestID))
			}
			if reqID != "" {
				hub.Scope().SetTag("request_id", reqID)
			}
			if path := c.FullPath(); path != "" {
				hub.Scope().SetTag("route", path)
			}
			hub.Scope().SetTag("method", c.Request.Method)
		}
	}
}

// SentryReportedKey 是用于在 gin.Context 中标记当前请求是否已向 Sentry 上报异常的键名。
const SentryReportedKey = "sentry_reported"

// MarkSentryReported 标记当前请求已向 Sentry / GlitchTip 上报过异常，避免重复告警。
func MarkSentryReported(c *gin.Context) {
	if c != nil {
		c.Set(SentryReportedKey, true)
	}
}

// IsSentryReported 检查当前请求是否已向 Sentry / GlitchTip 上报过异常。
func IsSentryReported(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, exists := c.Get(SentryReportedKey)
	if !exists {
		return false
	}
	reported, ok := v.(bool)
	return ok && reported
}

// captureHTTPError 向 Sentry / GlitchTip 上报 5xx 服务端错误（过滤正常 4xx 业务与入参校验错误）。
func captureHTTPError(c *gin.Context, err error) {
	if !platformsentry.IsInitialized() || err == nil {
		return
	}
	if c.Writer.Status() < 500 {
		return
	}
	if IsSentryReported(c) {
		return
	}
	MarkSentryReported(c)

	if hub := sentrygin.GetHubFromContext(c); hub != nil {
		hub.CaptureException(err)
		return
	}
	platformsentry.CaptureExceptionWithContext(c.Request.Context(), err)
}
