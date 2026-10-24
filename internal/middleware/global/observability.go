package global

import (
	"encoding/hex"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/logger"
)

// headerRequestID 是全链路追踪请求头。
const headerRequestID = "x-request-id"

// requestIDPrefix 是追踪标识前缀。
const requestIDPrefix = "req_"

// RequestID 【全局中间件】生成或透传全链路追踪请求标识。
func (m *Middleware) RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader(headerRequestID))
		if requestID == "" {
			// 对未显式传递追踪头的高频健康探针路由快速放行，规避高频 UUIDv7 生成与子日志器上下文分配开销
			if isProbeRequest(c) {
				c.Next()
				return
			}

			// 优先使用当前活跃 OTel Span 的 TraceID（实现请求标识、后端链路与日志三位一体统一）
			if span := trace.SpanFromContext(c.Request.Context()); span.SpanContext().IsValid() {
				requestID = span.SpanContext().TraceID().String()
			} else {
				requestID = newRequestID()
			}
		}

		c.Writer.Header().Set(headerRequestID, requestID)
		c.Request = c.Request.WithContext(logger.WithContext(c.Request.Context(), m.logger, requestID))
		c.Next()
	}
}

// newRequestID 生成形如 req_<uuidv7> 的追踪标识。
func newRequestID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return requestIDPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	}

	buf := make([]byte, 0, len(requestIDPrefix)+hex.EncodedLen(len(id)))
	buf = append(buf, requestIDPrefix...)
	return string(hex.AppendEncode(buf, id[:]))
}

// AccessLog 【全局中间件】记录每个请求的方法、路径、状态码与耗时。
func (m *Middleware) AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		isProbe := isProbeRequest(c)

		start := time.Now()
		c.Next()

		status := c.Writer.Status()

		// 健康检查探针正常状态（2xx/3xx）完全静默，避免高频心跳刷屏；异常（4xx/5xx）依然保留日志
		if isProbe && status < 400 {
			return
		}

		log := logger.FromContext(c.Request.Context())

		fields := make([]zap.Field, 0, 8)
		if errMsg := formatErrors(c.Errors); errMsg != "" {
			fields = append(fields, zap.String("error", errMsg))
		}
		if code := errs.ErrorCodeFromContext(c); code != "" {
			fields = append(fields, zap.String("code", code))
		}

		fields = append(fields,
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", status),
			zap.String("latency", time.Since(start).String()),
			zap.String("ip", c.ClientIP()),
		)

		switch {
		case status >= 500:
			log.Error("access", fields...)
		case status >= 400:
			log.Warn("access", fields...)
		default:
			log.Info("access", fields...)
		}
	}
}

// formatErrors 提取并拼接上下文错误文本，去除内部格式前缀与换行。
func formatErrors(errs []*gin.Error) string {
	if len(errs) == 0 {
		return ""
	}
	if len(errs) == 1 {
		return errs[0].Err.Error()
	}
	msgs := lo.Map(errs, func(e *gin.Error, _ int) string {
		return e.Err.Error()
	})
	return strings.Join(msgs, "; ")
}

// isProbeRequest 判断当前请求是否属于高频系统探针心跳路由。
func isProbeRequest(c *gin.Context) bool {
	return isProbePath(c.Request.URL.Path) || isProbePath(c.FullPath())
}

// isProbePath 判断请求是否为高频系统探针路由。
func isProbePath(path string) bool {
	return path == "/livez" || path == "/readyz" || path == "/healthz"
}

// Recovery 【全局中间件】捕获 panic 转为统一 500 响应，堆栈仅进入日志。
func (m *Middleware) Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			logger.FromContext(c.Request.Context()).Error("request panic recovered",
				zap.Any("panic", recovered),
				zap.String("stack", string(debug.Stack())))

			// 标记当前请求的 panic 异常已由 Zap/Sentry Core 捕获，防止全局 ErrorHandler 二次重复上报
			MarkSentryReported(c)

			if c.Writer.Written() {
				c.Abort()
				return
			}
			_ = c.Error(errs.Internal(nil))
			c.Abort()
		}()

		c.Next()
	}
}
