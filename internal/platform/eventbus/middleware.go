package eventbus

import (
	"cmp"
	"context"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"go.opentelemetry.io/otel/trace"
)

// MetadataKey 定义事件元数据标准字段键名。
const (
	MetadataTraceID     = "trace_id"
	MetadataSpanID      = "span_id"
	MetadataPublishedAt = "published_at"
)

type contextKey struct {
	name string
}

var (
	ctxKeyRequestID = contextKey{name: "request_id"}
)

// ContextWithRequestID 将 request_id / correlation_id 强类型绑定至上下文。
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, requestID)
}

// RequestIDFromContext 从上下文中安全提取 request_id。
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

// injectEventMetadata 在发布端将 OpenTelemetry TraceContext 与 CorrelationID 注入消息元数据。
func injectEventMetadata(params cqrs.OnEventSendParams) error {
	ctx := params.Message.Context()

	// 1. 优先提取并透传 OpenTelemetry TraceContext
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		sc := span.SpanContext()
		params.Message.Metadata.Set(MetadataTraceID, sc.TraceID().String())
		params.Message.Metadata.Set(MetadataSpanID, sc.SpanID().String())
	}

	// 2. 优先复用 Context 中已有的 CorrelationID / RequestID，避免高频无谓生成 UUID
	correlationID := cmp.Or(RequestIDFromContext(ctx), watermill.NewUUID())
	middleware.SetCorrelationID(correlationID, params.Message)

	// 3. 记录发布时间戳（RFC3339Nano），便于排查消费延迟与全链路度量
	params.Message.Metadata.Set(MetadataPublishedAt, time.Now().UTC().Format(time.RFC3339Nano))

	return nil
}

// traceAndCorrelationContextMiddleware 从消息 Metadata 中还原 TraceID 与 CorrelationID 并注入消费端 Context。
func traceAndCorrelationContextMiddleware(h message.HandlerFunc) message.HandlerFunc {
	return func(msg *message.Message) ([]*message.Message, error) {
		ctx := msg.Context()

		// 1. 还原 CorrelationID 与 RequestID 到 Context
		correlationID := middleware.MessageCorrelationID(msg)
		if correlationID != "" {
			ctx = ContextWithRequestID(ctx, correlationID)
		}

		// 2. 还原 TraceID 与 SpanID 到 Context（形成跨进程/异步远程 SpanContext）
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

		msg.SetContext(ctx)
		return h(msg)
	}
}
