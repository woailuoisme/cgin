package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"bgin/internal/middleware/global"
	"bgin/internal/platform/logger"
)

func TestTelemetryMiddleware_Disabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mw := global.New(global.Deps{})
	engine := gin.New()
	engine.Use(
		mw.Telemetry(),
		mw.RequestID(),
	)

	engine.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	engine.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("x-trace-id"))
	assert.NotEmpty(t, rec.Header().Get("x-request-id"))
}

func TestTelemetryMiddleware_TraceLogCorrelation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. 设置内存 TracerProvider 模拟已激活的 OpenTelemetry
	spanRecorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	var buf bytes.Buffer
	testLogger := logger.NewWithOptions(
		logger.WithLevel("info"),
		logger.WithChannel("json"),
		logger.WithWriter(&buf),
	)

	mw := global.New(global.Deps{Logger: testLogger})
	engine := gin.New()

	engine.Use(
		func(c *gin.Context) {
			// 模拟通过 Tracer 创建 Server Span 并下发上下文
			tr := tp.Tracer("test/http")
			ctx, span := tr.Start(c.Request.Context(), "GET /test-trace")
			defer span.End()
			c.Request = c.Request.WithContext(ctx)
			c.Next()
		},
		mw.RequestID(),
	)

	var recordedTraceID, recordedSpanID, recordedReqID string
	engine.GET("/test-trace", func(c *gin.Context) {
		log := logger.FromContext(c.Request.Context())
		log.Info("executing test handler")
		c.String(http.StatusOK, "ok")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test-trace", nil)
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	// 验证从活跃 Span 导出的 trace_id 是否已自动对齐为 X-Request-ID
	spans := spanRecorder.Ended()
	require.NotEmpty(t, spans)
	activeSpan := spans[0]
	expectedTraceID := activeSpan.SpanContext().TraceID().String()
	expectedSpanID := activeSpan.SpanContext().SpanID().String()

	assert.Equal(t, expectedTraceID, rec.Header().Get("x-request-id"), "未提供 X-Request-ID 时应自动对齐为 TraceID")

	// 验证 Zap JSON 日志是否已自动注入 trace_id 与 span_id
	line := strings.TrimSpace(buf.String())
	require.NotEmpty(t, line)

	var logData map[string]any
	err := json.Unmarshal([]byte(line), &logData)
	require.NoError(t, err)

	recordedTraceID, _ = logData["trace_id"].(string)
	recordedSpanID, _ = logData["span_id"].(string)
	recordedReqID, _ = logData["request_id"].(string)

	assert.Equal(t, expectedTraceID, recordedTraceID, "Zap 日志应包含匹配的 trace_id")
	assert.Equal(t, expectedSpanID, recordedSpanID, "Zap 日志应包含匹配的 span_id")
	assert.Equal(t, expectedTraceID, recordedReqID, "Zap 日志的 request_id 亦应统一对齐")
}
