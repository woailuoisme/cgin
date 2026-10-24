package config

import (
	"cmp"
	"fmt"
	"strings"
	"time"
)

// TelemetryConfig 描述 OpenTelemetry 遥测与全链路追踪配置。
type TelemetryConfig struct {
	Enabled        bool          `mapstructure:"otel_enabled"`
	Endpoint       string        `mapstructure:"otel_exporter_otlp_endpoint"`
	Insecure       bool          `mapstructure:"otel_insecure"`
	ServiceName    string        `mapstructure:"otel_service_name"`
	SampleRatio    float64       `mapstructure:"otel_traces_sample_ratio"`
	MetricInterval time.Duration `mapstructure:"otel_metric_interval"`
}

// EffectiveServiceName 获取有效服务标识名（优先 OTEL_SERVICE_NAME，回退应用基础配置 APP_NAME）。
func (t *TelemetryConfig) EffectiveServiceName(appName string) string {
	if s := strings.TrimSpace(t.ServiceName); s != "" {
		return s
	}
	return cmp.Or(appName, "bgin")
}

// EffectiveEndpoint 获取有效 Collector 目标地址（默认 http://127.0.0.1:4317）。
// 建议在生产环境中配置为标准 host:port 或带有 http(s):// 前缀的 gRPC 收集端点。
func (t *TelemetryConfig) EffectiveEndpoint() string {
	if ep := strings.TrimSpace(t.Endpoint); ep != "" {
		return ep
	}
	return "http://127.0.0.1:4317"
}

// EffectiveSampleRatio 获取有效追踪采样率（0.0 至 1.0）。
func (t *TelemetryConfig) EffectiveSampleRatio() float64 {
	if t.SampleRatio <= 0 {
		return 0
	}
	if t.SampleRatio >= 1.0 {
		return 1.0
	}
	return t.SampleRatio
}

// validateTelemetry 校验 Telemetry 配置项语义合法性。
func (c *Config) validateTelemetry() error {
	if !c.Telemetry.Enabled {
		return nil
	}
	if c.Telemetry.SampleRatio < 0.0 || c.Telemetry.SampleRatio > 1.0 {
		return fmt.Errorf("config: OTEL_TRACES_SAMPLE_RATIO must be between 0.0 and 1.0, got %f", c.Telemetry.SampleRatio)
	}
	if c.Telemetry.MetricInterval < 0 {
		return fmt.Errorf("config: OTEL_METRIC_INTERVAL cannot be negative, got %s", c.Telemetry.MetricInterval)
	}
	return nil
}
