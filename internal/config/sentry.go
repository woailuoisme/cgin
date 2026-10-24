package config

import (
	"strings"
)

// SentryConfig 描述 Sentry / GlitchTip 异常监控与性能追踪配置。
type SentryConfig struct {
	Enabled          *bool   `mapstructure:"sentry_enabled"`
	DSN              string  `mapstructure:"sentry_dsn"`
	GlitchTipDSN     string  `mapstructure:"glitchtip_dsn"`
	Environment      string  `mapstructure:"sentry_environment"`
	Release          string  `mapstructure:"sentry_release"`
	TracesSampleRate float64 `mapstructure:"sentry_traces_sample_rate"`
	Debug            bool    `mapstructure:"sentry_debug"`
}

// EffectiveDSN 获取有效 DSN（优先 SENTRY_DSN，回退 GLITCHTIP_DSN）。
func (s *SentryConfig) EffectiveDSN() string {
	if s == nil {
		return ""
	}
	if dsn := strings.TrimSpace(s.DSN); dsn != "" {
		return dsn
	}
	return strings.TrimSpace(s.GlitchTipDSN)
}

// IsEnabled 判断是否激活 Sentry / GlitchTip 监控。
func (s *SentryConfig) IsEnabled() bool {
	if s == nil || (s.Enabled != nil && !*s.Enabled) {
		return false
	}
	return s.EffectiveDSN() != ""
}

// validateSentry 校验 Sentry 配置项合法性。
func (c *Config) validateSentry() error {
	return nil
}
