package database

import (
	"cmp"
	"context"

	"github.com/uptrace/bun"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"bgin/internal/platform/telemetry"
)

// OTelQueryHook 为 Bun ORM 实现基于 OpenTelemetry 标准的分布式追踪查询钩子。
// 自动为每次数据库操作创建 Client 类型的 Span，记录 SQL 语句、操作类型与耗时。
type OTelQueryHook struct {
	tracer trace.Tracer
}

// NewOTelQueryHook 创建 OTelQueryHook 实例。
func NewOTelQueryHook() *OTelQueryHook {
	return &OTelQueryHook{
		tracer: telemetry.Tracer("bgin/database"),
	}
}

// BeforeQuery 在 SQL 执行前触发，创建 Client 级别的子 Span 并注入语义化属性。
func (h *OTelQueryHook) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	parent := trace.SpanFromContext(ctx)
	if !parent.SpanContext().IsValid() {
		return ctx
	}

	op := cmp.Or(event.Operation(), "query")
	spanName := "db." + op
	query := cmp.Or(event.QueryTemplate, event.Query)

	// 创建数据库 Client Span 并注入 ctx；Span 实例由 Bun ORM 隐式透传至 AfterQuery 中提取并关闭。
	ctx, _ = h.tracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.statement", query),
			attribute.String("db.operation", op),
		),
	)

	return ctx
}

// AfterQuery 在 SQL 执行完成后触发，记录执行异常并关闭 Span。
func (h *OTelQueryHook) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return
	}
	defer span.End()

	if isActionableDBError(event.Err) {
		span.RecordError(event.Err)
		span.SetStatus(codes.Error, event.Err.Error())
		return
	}

	span.SetStatus(codes.Ok, "")
}
