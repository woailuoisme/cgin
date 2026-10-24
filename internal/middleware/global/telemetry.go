package global

import (
	"cmp"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"bgin/internal/platform/telemetry"
)

const (
	headerTraceID = "x-trace-id"
	tracerScope   = "bgin/http"
	meterScope    = "bgin/http"
)

type httpMetrics struct {
	duration       metric.Float64Histogram
	requestsTotal  metric.Int64Counter
	activeRequests metric.Int64UpDownCounter
}

var (
	metricsOnce sync.Once
	metrics     *httpMetrics
)

// getHTTPMetrics 单例延迟初始化 HTTP 服务指标。
func getHTTPMetrics() *httpMetrics {
	metricsOnce.Do(func() {
		meter := telemetry.Meter(meterScope)

		duration, _ := meter.Float64Histogram(
			"http.server.request.duration",
			metric.WithDescription("Duration of HTTP server requests in milliseconds"),
			metric.WithUnit("ms"),
		)

		total, _ := meter.Int64Counter(
			"http.server.request.total",
			metric.WithDescription("Total number of HTTP requests processed"),
		)

		active, _ := meter.Int64UpDownCounter(
			"http.server.active_requests",
			metric.WithDescription("Number of concurrent active HTTP requests"),
		)

		metrics = &httpMetrics{
			duration:       duration,
			requestsTotal:  total,
			activeRequests: active,
		}
	})
	return metrics
}

// Telemetry 【全局中间件】负责 HTTP 链路追踪 (Traces) 上下文提取/注入与多维指标 (Metrics) 采集。
func (m *Middleware) Telemetry() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 若遥测未激活或为高频系统探针心跳，则零侵入快速放行，规避 Span 分配与度量计算
		if !telemetry.IsInitialized() || isProbeRequest(c) {
			c.Next()
			return
		}

		tr := telemetry.Tracer(tracerScope)
		httpM := getHTTPMetrics()

		// 1. 从入站 HTTP 请求头提取 W3C TraceContext 与 Baggage
		ctx := otel.GetTextMapPropagator().Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))

		// 2. 创建 Server 类型的根 Span
		spanName := c.Request.Method + " " + c.Request.URL.Path
		ctx, span := tr.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.method", c.Request.Method),
				attribute.String("http.client_ip", c.ClientIP()),
				attribute.String("http.user_agent", c.Request.UserAgent()),
			),
		)
		defer span.End()

		c.Writer.Header().Set(headerTraceID, span.SpanContext().TraceID().String())

		// 3. 将携带 TraceContext 的 context 注入 gin 请求上下文
		c.Request = c.Request.WithContext(ctx)

		// 4. 统计并发请求数
		if httpM.activeRequests != nil {
			httpM.activeRequests.Add(ctx, 1)
			defer httpM.activeRequests.Add(ctx, -1)
		}

		start := time.Now()
		c.Next()
		durMs := float64(time.Since(start)) / float64(time.Millisecond)

		status := c.Writer.Status()
		route := cmp.Or(c.FullPath(), "unmatched")

		// 5. 路由匹配后修正 Span 名称（如 "GET /v1/users/:id"），防止路径参数导致高基数
		span.SetName(c.Request.Method + " " + route)
		span.SetAttributes(
			attribute.String("http.route", route),
			attribute.Int("http.status_code", status),
		)

		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", status))
		} else {
			span.SetStatus(codes.Ok, "")
		}

		// 6. 记录请求耗时直方图与请求总量
		metricAttrs := metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("http.method", c.Request.Method),
			attribute.Int("http.status_code", status),
		)
		if httpM.duration != nil {
			httpM.duration.Record(ctx, durMs, metricAttrs)
		}
		if httpM.requestsTotal != nil {
			httpM.requestsTotal.Add(ctx, 1, metricAttrs)
		}
	}
}
