package global

import (
	"cmp"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-contrib/requestid"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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

// AccessLog 记录单行访问日志：方法、路径、状态码、耗时、来源 IP 与请求标识。
//
// 不再使用 gin-contrib/logger：它会硬带 body_size（恒为 -1）、user_agent 与一个指向
// 模块缓存的 caller，控制台噪音远大于信息量。request_id 已是 OTel TraceID，
// 不再重复输出 trace_id / span_id。
func (m *Middleware) AccessLog() gin.HandlerFunc {
	baseLog := cmp.Or(m.logger, logger.Default())

	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		// 探针 2xx/3xx 静默避免心跳刷屏，异常仍记录。
		if isProbeRequest(c) && status < http.StatusBadRequest {
			return
		}

		level := slog.LevelInfo
		switch {
		case status >= http.StatusInternalServerError:
			level = slog.LevelError
		case status >= http.StatusBadRequest:
			level = slog.LevelWarn
		}

		ctx := c.Request.Context()
		if !baseLog.Enabled(ctx, level) {
			return
		}

		attrs := make([]slog.Attr, 0, 8)
		if rid := requestIDOf(c); rid != "" {
			// 控制台开发模式下，普通 2xx/3xx 访问日志省略冗长 request_id，仅在 4xx/5xx 或出错时输出
			if m.production || status >= http.StatusBadRequest || len(c.Errors) > 0 {
				attrs = append(attrs, slog.String("request_id", rid))
			}
		}
		if errMsg := formatErrors(c.Errors); errMsg != "" {
			attrs = append(attrs, slog.String("error", errMsg))
		}
		if code := errs.ErrorCodeFromContext(c); code != "" {
			attrs = append(attrs, slog.String("code", code))
		}

		attrs = append(attrs,
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", status),
			slog.Duration("latency", time.Since(start)),
			slog.String("ip", c.ClientIP()),
		)

		baseLog.LogAttrs(ctx, level, "access", attrs...)
	}
}

// requestIDOf 优先取响应头回写的追踪标识，回退到 requestid 中间件的上下文值。
func requestIDOf(c *gin.Context) string {
	if rid := strings.TrimSpace(c.Writer.Header().Get(headerRequestID)); rid != "" {
		return rid
	}
	return requestid.Get(c)
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

			ctx := c.Request.Context()
			logger.FromContext(ctx).ErrorContext(ctx, "request panic recovered",
				slog.Any("panic", recovered),
				slog.String("stack", string(debug.Stack())),
			)

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
