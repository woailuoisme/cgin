package config

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// CORSConfig 描述跨域资源共享策略。未配置时不放行任何跨域来源。
type CORSConfig struct {
	AllowedOrigins []string `env:"CORS_ALLOWED_ORIGINS"`
}

// validateCORS 校验跨域来源白名单：仅接受通配符 * 或带 scheme 的来源，并在生产环境中拒绝通配符。
func (c *Config) validateCORS() error {
	if c.IsProduction() && slices.Contains(c.CORS.AllowedOrigins, "*") {
		return errors.New("config: CORS_ALLOWED_ORIGINS cannot be \"*\" in production, list explicit origins instead")
	}

	for _, origin := range c.CORS.AllowedOrigins {
		trimmed := strings.TrimSpace(origin)
		if trimmed == "" {
			return errors.New("config: CORS_ALLOWED_ORIGINS contains an empty entry, remove the redundant comma")
		}
		if trimmed == "*" {
			continue
		}

		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("config: CORS_ALLOWED_ORIGINS contains an invalid origin %q, expected \"*\" or \"scheme://host[:port]\"", origin)
		}
	}
	return nil
}
