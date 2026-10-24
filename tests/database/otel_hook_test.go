package database_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"bgin/internal/database"
)

func TestOTelQueryHook_NoActiveSpan(t *testing.T) {
	hook := database.NewOTelQueryHook()
	ctx := context.Background()

	event := &bun.QueryEvent{
		StartTime: time.Now(),
		Query:     "SELECT 1",
	}

	resCtx := hook.BeforeQuery(ctx, event)
	assert.Equal(t, ctx, resCtx)

	// AfterQuery 应优雅安全返回，无 panic
	assert.NotPanics(t, func() {
		hook.AfterQuery(resCtx, event)
	})
}

func TestOTelQueryHook_WithActiveSpan(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	hook := database.NewOTelQueryHook()

	// 开启根 Span
	tr := tp.Tracer("test")
	rootCtx, rootSpan := tr.Start(context.Background(), "root-operation")

	event := &bun.QueryEvent{
		StartTime:     time.Now(),
		QueryTemplate: "SELECT * FROM users WHERE id = ?",
		Query:         "SELECT * FROM users WHERE id = 1",
	}

	queryCtx := hook.BeforeQuery(rootCtx, event)
	assert.NotEqual(t, rootCtx, queryCtx)

	hook.AfterQuery(queryCtx, event)
	rootSpan.End()

	spans := spanRecorder.Ended()
	require.Len(t, spans, 2)

	// 子 Span 为 db 查询 Span
	dbSpan := spans[0]
	assert.Equal(t, "db.SELECT", dbSpan.Name())
	assert.Equal(t, codes.Ok, dbSpan.Status().Code)

	var hasDBSystem, hasDBStatement bool
	for _, attr := range dbSpan.Attributes() {
		if attr.Key == "db.system" && attr.Value.AsString() == "postgresql" {
			hasDBSystem = true
		}
		if attr.Key == "db.statement" && attr.Value.AsString() == "SELECT * FROM users WHERE id = ?" {
			hasDBStatement = true
		}
	}
	assert.True(t, hasDBSystem, "应设置 db.system=postgresql 属性")
	assert.True(t, hasDBStatement, "应设置 db.statement 属性")
}

func TestOTelQueryHook_WithError(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	hook := database.NewOTelQueryHook()

	tr := tp.Tracer("test")
	rootCtx, rootSpan := tr.Start(context.Background(), "root-operation")

	event := &bun.QueryEvent{
		StartTime: time.Now(),
		Query:     "INSERT INTO accounts ...",
		Err:       errors.New("db connection timeout"),
	}

	queryCtx := hook.BeforeQuery(rootCtx, event)
	hook.AfterQuery(queryCtx, event)
	rootSpan.End()

	spans := spanRecorder.Ended()
	require.Len(t, spans, 2)

	dbSpan := spans[0]
	assert.Equal(t, codes.Error, dbSpan.Status().Code)
	assert.Equal(t, "db connection timeout", dbSpan.Status().Description)

	// 验证 sql.ErrNoRows 不视为 Span 错误
	rootCtx2, rootSpan2 := tr.Start(context.Background(), "root-2")
	event2 := &bun.QueryEvent{
		StartTime: time.Now(),
		Query:     "SELECT * FROM accounts WHERE id = 999",
		Err:       sql.ErrNoRows,
	}
	queryCtx2 := hook.BeforeQuery(rootCtx2, event2)
	hook.AfterQuery(queryCtx2, event2)
	rootSpan2.End()

	spans = spanRecorder.Ended()
	require.Len(t, spans, 4)
	dbSpan2 := spans[2]
	assert.Equal(t, codes.Ok, dbSpan2.Status().Code, "sql.ErrNoRows 属于正常业务未命中，不应标记为 Span 错误")
}
