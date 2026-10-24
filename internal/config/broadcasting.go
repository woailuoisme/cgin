// Package config 声明 Centrifugo 实时通信服务端连接配置。
package config

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// CentrifugoConfig 描述 Centrifugo 实时通信服务连接配置。
type CentrifugoConfig struct {
	// Enabled 控制是否启用实时通信服务。
	Enabled bool `env:"CENTRIFUGO_ENABLED"`
	// URL Centrifugo 服务端 HTTP API 地址。
	URL string `env:"CENTRIFUGO_URL"`
	// APIKey 服务端调用 Centrifugo HTTP API 凭据。
	APIKey string `env:"CENTRIFUGO_API_KEY"`
	// Secret 用于 HMAC-SHA256 签发客户端 Token 及校验请求签名的密钥。
	Secret string `env:"CENTRIFUGO_SECRET"`
	// TokenTTL 签发给客户端的连接凭据有效期。
	TokenTTL time.Duration `env:"CENTRIFUGO_TOKEN_TTL" envDefault:"2h"`
}

// RealtimeConfig 为 CentrifugoConfig 别名。
type RealtimeConfig = CentrifugoConfig

// validateRealtime 校验 Centrifugo 配置有效性。
func (c *Config) validateRealtime() error {
	if !c.Realtime.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Realtime.URL) == "" {
		return errors.New("config: CENTRIFUGO_URL cannot be empty when realtime is enabled")
	}
	if _, err := url.ParseRequestURI(c.Realtime.URL); err != nil {
		return errors.New("config: invalid CENTRIFUGO_URL format")
	}
	if strings.TrimSpace(c.Realtime.Secret) == "" {
		return errors.New("config: CENTRIFUGO_SECRET cannot be empty when realtime is enabled")
	}
	if strings.TrimSpace(c.Realtime.APIKey) == "" {
		return errors.New("config: CENTRIFUGO_API_KEY cannot be empty when realtime is enabled")
	}
	return nil
}
