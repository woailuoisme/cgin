package config

import (
	"errors"
	"fmt"
	"time"
)

// JWTConfig 描述双令牌签名与有效时长（对齐 Laravel config/jwt.php）。
type JWTConfig struct {
	Secret     string        `mapstructure:"jwt_secret"`
	Issuer     string        `mapstructure:"jwt_issuer"`
	AccessTTL  time.Duration `mapstructure:"jwt_ttl"`
	RefreshTTL time.Duration `mapstructure:"jwt_refresh_ttl"`
}

// validateJWT 检查令牌密钥长度与有效时长合理性。
func (c *Config) validateJWT() error {
	if len(c.JWT.Secret) < 32 {
		return fmt.Errorf("config: JWT_SECRET must be at least 32 characters, got %d", len(c.JWT.Secret))
	}
	if c.JWT.AccessTTL <= 0 || c.JWT.RefreshTTL <= 0 {
		return errors.New("config: JWT_TTL and JWT_REFRESH_TTL must be positive")
	}
	if c.JWT.RefreshTTL <= c.JWT.AccessTTL {
		return errors.New("config: JWT_REFRESH_TTL must be greater than JWT_TTL")
	}
	return nil
}
