package global

import (
	"cmp"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"cgin/pkg/telemetry"
)

const (
	headerTraceID = "x-trace-id"
	tracerScope   = "cgin/http"
	meterScope    = "cgin/http"
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

// Telemetry 负责 HTTP 链路追踪上下文提取/注入与请求指标采集。
func (m *Middleware) Telemetry() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 遥测未启用或探测心跳时直接放行，省掉 Span 与度量开销。
		if !telemetry.IsInitialized() || isProbeRequest(c) {
			c.Next()
			return
		}

		tr := telemetry.Tracer(tracerScope)
		httpM := getHTTPMetrics()

		// 从入站请求头提取 W3C TraceContext 与 Baggage。
		ctx := otel.GetTextMapPropagator().Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))

		// 建立 Server 类型的根 Span。
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
		c.Request = c.Request.WithContext(ctx)

		if httpM.activeRequests != nil {
			httpM.activeRequests.Add(ctx, 1)
			defer httpM.activeRequests.Add(ctx, -1)
		}

		start := time.Now()
		c.Next()
		durMs := float64(time.Since(start)) / float64(time.Millisecond)

		status := c.Writer.Status()
		route := cmp.Or(c.FullPath(), "unmatched")

		// 路由匹配后再命名，避免路径参数造成指标高基数。
		span.SetName(c.Request.Method + " " + route)
		span.SetAttributes(
			attribute.String("http.route", route),
			attribute.Int("http.status_code", status),
		)

		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, "HTTP "+strconv.Itoa(status))
		} else {
			span.SetStatus(codes.Ok, "")
		}

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
