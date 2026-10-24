package config

import "fmt"

// LimiterConfig 描述请求速率限制配额（每分钟最大请求数，0 为不限制）。
type LimiterConfig struct {
	Enabled       *bool `env:"RATELIMIT_ENABLED" envDefault:"true"`
	Anonymous     int   `env:"RATELIMIT_ANONYMOUS" envDefault:"60"`
	Authenticated int   `env:"RATELIMIT_AUTHENTICATED" envDefault:"300"`
	Sensitive     int   `env:"RATELIMIT_SENSITIVE" envDefault:"10"`
}

// IsEnabled 报告是否全局激活请求速率限制保护。
// 当显式配置了 *Enabled 时遵循该值；未设置（如代码零值初始化）时默认视为启用 (true)。
func (l LimiterConfig) IsEnabled() bool {
	return l.Enabled == nil || *l.Enabled
}

// validateLimiter 校验速率限制配额：0 表示显式关闭，负值属于非法配置。
func (c *Config) validateLimiter() error {
	tiers := []struct {
		name  string
		value int
	}{
		{"RATELIMIT_ANONYMOUS", c.Limiter.Anonymous},
		{"RATELIMIT_AUTHENTICATED", c.Limiter.Authenticated},
		{"RATELIMIT_SENSITIVE", c.Limiter.Sensitive},
	}
	for _, tier := range tiers {
		if tier.value < 0 {
			return fmt.Errorf("config: %s cannot be negative, use 0 to disable the tier explicitly", tier.name)
		}
	}
	return nil
}
