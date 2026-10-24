// Package telemetry 提供基于 OpenTelemetry 标准的分布式追踪、指标度量与资源上下文装配。
package telemetry

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap/zapcore"

	"bgin/internal/config"
)

const defaultShutdownTimeout = 5 * time.Second

var (
	mu          sync.RWMutex
	initialized bool
	tracerProv  *sdktrace.TracerProvider
	meterProv   *sdkmetric.MeterProvider
	loggerProv  *sdklog.LoggerProvider
)

// Init 依据配置初始化 OpenTelemetry TracerProvider、MeterProvider 与运行时指标采集。
// 若未开启（cfg.Enabled 为 false），则保持静默未初始化状态，返回空操作清理函数。
func Init(
	ctx context.Context,
	cfg config.TelemetryConfig,
	appName, appEnv, appVersion string,
) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	serviceName := cfg.EffectiveServiceName(appName)
	endpoint, insecure := parseEndpoint(cfg.EffectiveEndpoint(), cfg.Insecure)

	// 1. 构建标准语义资源描述 (Resource)
	attrs := []resource.Option{
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.DeploymentEnvironmentKey.String(appEnv),
		),
		resource.WithProcess(),
		resource.WithOS(),
		resource.WithHost(),
	}
	if appVersion != "" {
		attrs = append(attrs, resource.WithAttributes(semconv.ServiceVersionKey.String(appVersion)))
	}

	res, err := resource.New(ctx, attrs...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: build resource failed: %w", err)
	}

	// 2. 初始化 OTLP/gRPC Traces 导出器与 TracerProvider
	traceOpts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
	}
	if insecure {
		traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
	}

	traceExporter, err := otlptracegrpc.New(ctx, traceOpts...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: init trace exporter failed: %w", err)
	}

	sampler := resolveSampler(cfg.EffectiveSampleRatio())

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	// 配置全局 W3C 链路传播器 (TraceContext + Baggage)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// 3. 初始化 OTLP/gRPC Metrics 导出器与 MeterProvider
	metricOpts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(endpoint),
	}
	if insecure {
		metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
	}

	metricExporter, err := otlpmetricgrpc.New(ctx, metricOpts...)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("telemetry: init metric exporter failed: %w", err)
	}

	interval := cmp.Or(cfg.MetricInterval, 15*time.Second)

	reader := sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(interval))
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	// 4. 开启 Go 运行时指标采集 (GC, Heap, Goroutines, Allocations)
	if err := runtime.Start(
		runtime.WithMeterProvider(mp),
		runtime.WithMinimumReadMemStatsInterval(interval),
	); err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, fmt.Errorf("telemetry: start runtime instrumentation failed: %w", err)
	}

	// 5. 初始化 OTLP/gRPC Logs 导出器与 LoggerProvider
	logOpts := []otlploggrpc.Option{
		otlploggrpc.WithEndpoint(endpoint),
	}
	if insecure {
		logOpts = append(logOpts, otlploggrpc.WithInsecure())
	}

	logExporter, err := otlploggrpc.New(ctx, logOpts...)
	if err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, fmt.Errorf("telemetry: init log exporter failed: %w", err)
	}

	processor := sdklog.NewBatchProcessor(logExporter)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(processor),
		sdklog.WithResource(res),
	)
	otel.SetLoggerProvider(lp)

	mu.Lock()
	initialized = true
	tracerProv = tp
	meterProv = mp
	loggerProv = lp
	mu.Unlock()

	return Shutdown, nil
}

// parseEndpoint 解析 OTLP 目标端点地址，去除协议前缀并推导是否使用明文传输。
func parseEndpoint(raw string, forceInsecure bool) (string, bool) {
	insecure := forceInsecure || strings.HasPrefix(raw, "http://")
	endpoint := strings.TrimPrefix(raw, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	return strings.TrimRight(endpoint, "/"), insecure
}

// resolveSampler 解析并创建对应的 OTel 采样策略。
func resolveSampler(ratio float64) sdktrace.Sampler {
	if ratio >= 1.0 {
		return sdktrace.AlwaysSample()
	}
	if ratio <= 0.0 {
		return sdktrace.NeverSample()
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

// IsInitialized 检查 OpenTelemetry 基础设施是否已成功加载并就绪。
func IsInitialized() bool {
	mu.RLock()
	defer mu.RUnlock()
	return initialized
}

// Tracer 返回指定命名空间的全局 Tracer。若未就绪，返回默认 Noop/全局兜底 Tracer。
func Tracer(name string) trace.Tracer {
	return otel.GetTracerProvider().Tracer(name)
}

// Meter 返回指定命名空间的全局 Meter。若未就绪，返回默认 Noop/全局兜底 Meter。
func Meter(name string) metric.Meter {
	return otel.GetMeterProvider().Meter(name)
}

// Shutdown 优雅停机并冲刷所有待发送的链路追踪、指标度量与日志批处理缓冲区。
func Shutdown(ctx context.Context) error {
	mu.Lock()
	if !initialized {
		mu.Unlock()
		return nil
	}
	tp := tracerProv
	mp := meterProv
	lp := loggerProv
	tracerProv = nil
	meterProv = nil
	loggerProv = nil
	initialized = false
	mu.Unlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
	}

	var errs []error
	if tp != nil {
		if err := tp.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown tracer provider: %w", err))
		}
	}
	if mp != nil {
		if err := mp.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown meter provider: %w", err))
		}
	}
	if lp != nil {
		if err := lp.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown logger provider: %w", err))
		}
	}

	return errors.Join(errs...)
}

// zapCore 包装 otelzap.Core，以遵循配置的最小日志级别。
type zapCore struct {
	zapcore.Core
	minLevel zapcore.Level
}

func (c *zapCore) Enabled(lvl zapcore.Level) bool {
	return lvl >= c.minLevel && c.Core.Enabled(lvl)
}

func (c *zapCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *zapCore) With(fields []zapcore.Field) zapcore.Core {
	return &zapCore{
		Core:     c.Core.With(fields),
		minLevel: c.minLevel,
	}
}

// NewZapCore 构建将 Zap 结构化日志桥接导出至 OpenTelemetry 的 zapcore.Core。
// 若未开启 Telemetry 或未就绪，返回 nil。
func NewZapCore(serviceName string, minLevel zapcore.Level) zapcore.Core {
	mu.RLock()
	defer mu.RUnlock()
	if !initialized || loggerProv == nil {
		return nil
	}
	core := otelzap.NewCore(
		serviceName,
		otelzap.WithLoggerProvider(loggerProv),
	)
	return &zapCore{
		Core:     core,
		minLevel: minLevel,
	}
}
