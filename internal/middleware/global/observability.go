package global

import (
	"cmp"
	"encoding/hex"
	"runtime/debug"
	"strings"

	ginlogger "github.com/gin-contrib/logger"
	"github.com/gin-contrib/requestid"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/samber/lo"
	"go.opentelemetry.io/otel/trace"

	"cgin/pkg/errs"
	"cgin/pkg/logger"
)

// headerRequestID 是全链路追踪请求头。
const headerRequestID = "x-request-id"

// requestIDPrefix 是追踪标识前缀。
const requestIDPrefix = "req_"

// RequestID 基于 gin-contrib/requestid 生成或透传全链路请求标识。
func (m *Middleware) RequestID() gin.HandlerFunc {
	base := requestid.New(
		requestid.WithCustomHeaderStrKey(headerRequestID),
		requestid.WithGenerator(newRequestID),
		requestid.WithHandler(func(c *gin.Context, rid string) {
			c.Request = c.Request.WithContext(logger.WithContext(c.Request.Context(), m.logger, rid))
		}),
	)

	return func(c *gin.Context) {
		// 探针无外部追踪头时跳过，省掉 UUIDv7 生成与子日志器分配。
		if isProbeRequest(c) && strings.TrimSpace(c.GetHeader(headerRequestID)) == "" {
			c.Next()
			return
		}

		// 已有 OTel Span 时复用其 TraceID 作为请求标识。
		if strings.TrimSpace(c.GetHeader(headerRequestID)) == "" {
			if span := trace.SpanFromContext(c.Request.Context()); span.SpanContext().IsValid() {
				c.Request.Header.Set(headerRequestID, span.SpanContext().TraceID().String())
			}
		}

		base(c)
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

// AccessLog 基于 gin-contrib/logger 记录请求方法、路径、状态码与耗时。
func (m *Middleware) AccessLog() gin.HandlerFunc {
	baseLog := cmp.Or(m.logger, logger.Default())

	return ginlogger.SetLogger(
		ginlogger.WithUTC(true),
		ginlogger.WithMessage("access"),
		ginlogger.WithDefaultLevel(zerolog.InfoLevel),
		ginlogger.WithClientErrorLevel(zerolog.WarnLevel),
		ginlogger.WithServerErrorLevel(zerolog.ErrorLevel),
		ginlogger.WithSkipper(func(c *gin.Context) bool {
			// 探针 2xx/3xx 静默避免心跳刷屏，异常仍记录。
			return isProbeRequest(c) && c.Writer.Status() < 400
		}),
		ginlogger.WithLogger(func(c *gin.Context, _ zerolog.Logger) zerolog.Logger {
			reqLogger := *baseLog
			rid := strings.TrimSpace(c.Writer.Header().Get(headerRequestID))
			if rid == "" {
				rid = requestid.Get(c)
			}
			if rid != "" {
				reqLogger = reqLogger.With().Str("request_id", rid).Logger()
			}
			if span := trace.SpanFromContext(c.Request.Context()); span.SpanContext().IsValid() {
				reqLogger = reqLogger.With().
					Str("trace_id", span.SpanContext().TraceID().String()).
					Str("span_id", span.SpanContext().SpanID().String()).
					Logger()
			}
			return reqLogger
		}),
		ginlogger.WithContext(func(c *gin.Context, e *zerolog.Event) *zerolog.Event {
			if errMsg := formatErrors(c.Errors); errMsg != "" {
				e = e.Str("error", errMsg)
			}
			if code := errs.ErrorCodeFromContext(c); code != "" {
				e = e.Str("code", code)
			}
			return e
		}),
	)
}

// formatErrors 拼接上下文错误文本。
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

// isProbeRequest 判断是否为高频探针路由（官方 skipper 无此语义，属项目自建）。
func isProbeRequest(c *gin.Context) bool {
	return isProbePath(c.Request.URL.Path) || isProbePath(c.FullPath())
}

// isProbePath 判断请求是否为高频系统探针路由。
func isProbePath(path string) bool {
	return path == "/livez" || path == "/readyz" || path == "/healthz"
}

// Recovery 捕获 panic 转为统一 500 信封，堆栈仅进日志。
// 官方 Recovery 绕过 code/message 契约，故保留自建。
func (m *Middleware) Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			logger.FromContext(c.Request.Context()).Error().
				Any("panic", recovered).
				Str("stack", string(debug.Stack())).
				Msg("request panic recovered")

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
