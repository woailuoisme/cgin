package eventbus

import (
	"cmp"
	"context"
	"log/slog"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"go.opentelemetry.io/otel/trace"

	"cgin/pkg/errs"
	"cgin/pkg/logger"
)

// 事件消息 Metadata 标准键名。
const (
	MetadataTraceID     = "trace_id"
	MetadataSpanID      = "span_id"
	MetadataPublishedAt = "published_at"
)

// LoggerAdapter 将系统统一的 *slog.Logger 适配为 Watermill 的 LoggerAdapter 接口。
type LoggerAdapter struct {
	logger   *slog.Logger
	minLevel slog.Level
}

var _ watermill.LoggerAdapter = (*LoggerAdapter)(nil)

// NewLoggerAdapter 创建 Watermill 日志适配器，支持按最小级别过滤内部流水，nil 日志器自动降级为静默输出。
func NewLoggerAdapter(l *slog.Logger, minLevel ...slog.Level) *LoggerAdapter {
	lvl := slog.LevelWarn
	if len(minLevel) > 0 {
		lvl = minLevel[0]
	}
	return &LoggerAdapter{
		logger:   cmp.Or(l, logger.Nop()),
		minLevel: lvl,
	}
}

func fieldsToAttrs(fields watermill.LogFields) []any {
	if len(fields) == 0 {
		return nil
	}
	attrs := make([]any, 0, len(fields))
	for k, v := range fields {
		attrs = append(attrs, slog.Any(k, v))
	}
	return attrs
}

// Error 记录错误级别日志。
func (a *LoggerAdapter) Error(msg string, err error, fields watermill.LogFields) {
	if a.minLevel > slog.LevelError || !a.logger.Enabled(context.Background(), slog.LevelError) {
		return
	}
	extra := 0
	if err != nil {
		extra = 1
	}
	attrs := make([]any, 0, len(fields)+extra)
	if err != nil {
		attrs = append(attrs, slog.Any("error", err))
	}
	for k, v := range fields {
		attrs = append(attrs, slog.Any(k, v))
	}
	a.logger.Error(msg, attrs...)
}

// Info 记录信息级别日志。
func (a *LoggerAdapter) Info(msg string, fields watermill.LogFields) {
	if a.minLevel > slog.LevelInfo || !a.logger.Enabled(context.Background(), slog.LevelInfo) {
		return
	}
	a.logger.Info(msg, fieldsToAttrs(fields)...)
}

// Debug 记录调试级别日志。
func (a *LoggerAdapter) Debug(msg string, fields watermill.LogFields) {
	if a.minLevel > slog.LevelDebug || !a.logger.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	a.logger.Debug(msg, fieldsToAttrs(fields)...)
}

// Trace 记录跟踪级别日志。
func (a *LoggerAdapter) Trace(msg string, fields watermill.LogFields) {
	if a.minLevel > slog.LevelDebug || !a.logger.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	a.logger.Debug(msg, fieldsToAttrs(fields)...)
}

// With 返回携带指定上下文字段的新适配器实例。
func (a *LoggerAdapter) With(fields watermill.LogFields) watermill.LoggerAdapter {
	if len(fields) == 0 {
		return a
	}
	return &LoggerAdapter{
		logger:   a.logger.With(fieldsToAttrs(fields)...),
		minLevel: a.minLevel,
	}
}

// injectEventMetadata 在发布端将 OpenTelemetry TraceContext 与 logger.RequestIDFromContext 注入消息元数据。
func injectEventMetadata(params cqrs.OnEventSendParams) error {
	ctx := params.Message.Context()

	// 1. 提取并透传 OpenTelemetry TraceContext
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		sc := span.SpanContext()
		params.Message.Metadata.Set(MetadataTraceID, sc.TraceID().String())
		params.Message.Metadata.Set(MetadataSpanID, sc.SpanID().String())
	}

	// 2. 复用 pkg/logger 上下文中的 request_id 作为 CorrelationID，缺失时生成新 UUID
	correlationID := cmp.Or(logger.RequestIDFromContext(ctx), watermill.NewUUID())
	middleware.SetCorrelationID(correlationID, params.Message)

	// 3. 记录发布时间戳（RFC3339Nano），便于观测端到端消费延迟
	params.Message.Metadata.Set(MetadataPublishedAt, time.Now().UTC().Format(time.RFC3339Nano))

	return nil
}

// observabilityContextMiddleware 在消费端还原远程 OpenTelemetry SpanContext，
// 并通过 logger.WithContext 注入携带 request_id / trace_id / span_id 的请求作用域日志器。
func observabilityContextMiddleware(baseLogger *slog.Logger) message.HandlerMiddleware {
	return func(h message.HandlerFunc) message.HandlerFunc {
		return func(msg *message.Message) ([]*message.Message, error) {
			ctx := msg.Context()

			// 1. 先还原 OpenTelemetry SpanContext，使后续 logger.WithContext 自动感知 trace_id 与 span_id
			traceIDStr := msg.Metadata.Get(MetadataTraceID)
			spanIDStr := msg.Metadata.Get(MetadataSpanID)
			if traceIDStr != "" {
				if traceID, err := trace.TraceIDFromHex(traceIDStr); err == nil {
					var spanID trace.SpanID
					if spanIDStr != "" {
						spanID, _ = trace.SpanIDFromHex(spanIDStr)
					}
					spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
						TraceID:    traceID,
						SpanID:     spanID,
						TraceFlags: trace.FlagsSampled,
						Remote:     true,
					})
					ctx = trace.ContextWithSpanContext(ctx, spanCtx)
				}
			}

			// 2. 注入统一请求作用域日志器与 request_id（监听器调用 logger.FromContext(ctx) 即可自动携带全链路字段）
			correlationID := middleware.MessageCorrelationID(msg)
			ctx = logger.WithContext(ctx, baseLogger, correlationID)

			msg.SetContext(ctx)
			return h(msg)
		}
	}
}

// finalErrorBoundaryMiddleware 挂载在 Retry 外层：
// 当事件处理器经指数退避重试耗尽仍失败时，提取 oops 结构化属性与堆栈记录 Error 日志，
// 并返回 nil 确认消费完毕，防止内存 gochannel 因 Nack 陷入死循环重投。
func finalErrorBoundaryMiddleware() message.HandlerMiddleware {
	return func(h message.HandlerFunc) message.HandlerFunc {
		return func(msg *message.Message) ([]*message.Message, error) {
			msgs, err := h(msg)
			if err == nil {
				return msgs, nil
			}

			ctx := msg.Context()
			attrs := []slog.Attr{
				slog.Any("error", errs.RootCause(err)),
				slog.String("message_uuid", msg.UUID),
				slog.String("correlation_id", middleware.MessageCorrelationID(msg)),
			}
			if stack := errs.Stack(err); stack != "" {
				attrs = append(attrs, slog.String("stack", stack))
			}
			for _, f := range errs.OopsFields(err) {
				attrs = append(attrs, slog.Any(f.Key, f.Value))
			}
			logger.FromContext(ctx).LogAttrs(ctx, slog.LevelError, "event handler failed after exhausting retries", attrs...)

			return nil, nil
		}
	}
}
