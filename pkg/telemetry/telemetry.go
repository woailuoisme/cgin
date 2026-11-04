// Package telemetry 提供基于 OpenTelemetry 标准的分布式追踪、指标度量与资源上下文装配。
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"cgin/internal/config"
)

const defaultShutdownTimeout = 5 * time.Second

// initialized 记录全局 OpenTelemetry 基础设施是否已激活，支持中间件零锁快速判定。
var initialized atomic.Bool

// Provider 持有 OpenTelemetry 链路追踪、指标度量与日志导出实例，
// 原生实现 Shutdown(context.Context) error 契约供 DI 容器直接托管生命周期。
type Provider struct {
	enabled    atomic.Bool
	tracerProv *sdktrace.TracerProvider
	meterProv  *sdkmetric.MeterProvider
	loggerProv *sdklog.LoggerProvider
}

// New 依据配置构建并初始化 OpenTelemetry Provider（TracerProvider、MeterProvider、LoggerProvider 与运行时指标采集）。
// 若未开启（cfg.Enabled 为 false），返回静默的空操作 Provider 实例。
func New(
	ctx context.Context,
	cfg config.TelemetryConfig,
	appEnv, appVersion string,
) (*Provider, error) {
	if !cfg.Enabled {
		return &Provider{}, nil
	}

	cfg.Normalize("")
	endpoint, insecure := parseEndpoint(cfg.Endpoint, cfg.Insecure)

	// 1. 构建标准语义资源描述 (Resource)
	attrs := []resource.Option{
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
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

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(resolveSampler(cfg.SampleRatio)),
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

	interval := cfg.MetricInterval
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

	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		sdklog.WithResource(res),
	)
	otel.SetLoggerProvider(lp)

	p := &Provider{
		tracerProv: tp,
		meterProv:  mp,
		loggerProv: lp,
	}
	p.enabled.Store(true)
	initialized.Store(true)
	return p, nil
}

// Enabled 返回当前 Provider 是否已启用遥测采集。
func (p *Provider) Enabled() bool {
	return p != nil && p.enabled.Load()
}

// ZerologHook 构建将 Zerolog 结构化日志桥接导出至 OpenTelemetry 的 zerolog.Hook。
// 若未开启 Telemetry，返回 nil。
func (p *Provider) ZerologHook(serviceName string, minLevel zerolog.Level) zerolog.Hook {
	if !p.Enabled() || p.loggerProv == nil {
		return nil
	}
	return &zerologHook{
		logger:   p.loggerProv.Logger(serviceName),
		minLevel: minLevel,
	}
}

// Shutdown 优雅停机并冲刷所有待发送的链路追踪、指标度量与日志批处理缓冲区。
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || !p.enabled.CompareAndSwap(true, false) {
		return nil
	}
	initialized.Store(false)

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
	}

	var errs []error
	if p.tracerProv != nil {
		if err := p.tracerProv.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown tracer provider: %w", err))
		}
	}
	if p.meterProv != nil {
		if err := p.meterProv.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown meter provider: %w", err))
		}
	}
	if p.loggerProv != nil {
		if err := p.loggerProv.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown logger provider: %w", err))
		}
	}

	return errors.Join(errs...)
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
	return initialized.Load()
}

// Tracer 返回指定命名空间的全局 Tracer。若未就绪，返回默认 Noop/全局兜底 Tracer。
func Tracer(name string) trace.Tracer {
	return otel.GetTracerProvider().Tracer(name)
}

// Meter 返回指定命名空间的全局 Meter。若未就绪，返回默认 Noop/全局兜底 Meter。
func Meter(name string) metric.Meter {
	return otel.GetMeterProvider().Meter(name)
}

// zerologHook 将 Zerolog 事件桥接导出至 OpenTelemetry 日志记录器。
type zerologHook struct {
	logger   otellog.Logger
	minLevel zerolog.Level
}

var _ zerolog.Hook = (*zerologHook)(nil)

func (h *zerologHook) Run(_ *zerolog.Event, level zerolog.Level, msg string) {
	if level < h.minLevel || level == zerolog.NoLevel {
		return
	}
	var r otellog.Record
	now := time.Now()
	r.SetTimestamp(now)
	r.SetObservedTimestamp(now)
	r.SetSeverity(toOTelSeverity(level))
	r.SetSeverityText(level.String())
	r.SetBody(attribute.StringValue(msg))
	h.logger.Emit(context.Background(), r)
}

func toOTelSeverity(lvl zerolog.Level) otellog.Severity {
	switch lvl {
	case zerolog.DebugLevel, zerolog.TraceLevel:
		return otellog.SeverityDebug
	case zerolog.InfoLevel:
		return otellog.SeverityInfo
	case zerolog.WarnLevel:
		return otellog.SeverityWarn
	case zerolog.ErrorLevel:
		return otellog.SeverityError
	case zerolog.PanicLevel:
		return otellog.SeverityFatal1
	case zerolog.FatalLevel:
		return otellog.SeverityFatal2
	default:
		return otellog.SeverityUndefined
	}
}
