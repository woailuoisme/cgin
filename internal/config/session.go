package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SessionSecretMinLength 是会话签名密钥的最小长度（AES-256 强度，32 字节）。
const SessionSecretMinLength = 32

// defaultSessionMaxAge 是未配置有效期时的兜底值（7 天）。
const defaultSessionMaxAge = 7 * 24 * time.Hour

// SessionConfig 描述会话 Cookie 的名称、签名密钥与有效期。
//
// 密钥必须来自环境变量：生产环境缺失或过短会在启动期直接失败，
// 非生产环境缺省时由 validateSession 生成一次性随机密钥并打警告，杜绝静态弱密钥。
type SessionConfig struct {
	// Name 是会话 Cookie 名称。
	Name string `env:"SESSION_NAME" envDefault:"cgin_session"`
	// Secret 是会话签名密钥；生产必填，长度不低于 SessionSecretMinLength。
	Secret string `env:"SESSION_SECRET"`
	// MaxAge 是会话有效期。
	MaxAge time.Duration `env:"SESSION_MAX_AGE" envDefault:"168h"`
	// Generated 由校验阶段回填：标记密钥是否为非生产环境自动生成。
	Generated bool `env:"-"`
}

// validateSession 校验会话配置，并在非生产环境缺省密钥时回填一次性随机密钥。
func (c *Config) validateSession() error {
	name := strings.TrimSpace(c.Session.Name)
	if name == "" {
		return errors.New("config: SESSION_NAME cannot be empty")
	}
	c.Session.Name = name

	if c.Session.MaxAge <= 0 {
		if c.Session.MaxAge == 0 {
			c.Session.MaxAge = defaultSessionMaxAge
		} else {
			return errors.New("config: SESSION_MAX_AGE must be a positive duration")
		}
	}

	secret := strings.TrimSpace(c.Session.Secret)
	if c.IsProduction() {
		if secret == "" {
			return errors.New("config: SESSION_SECRET is required in production, generate one with \"openssl rand -hex 32\"")
		}
		if len(secret) < SessionSecretMinLength {
			return fmt.Errorf("config: SESSION_SECRET must be at least %d characters in production", SessionSecretMinLength)
		}
		c.Session.Secret = secret
		return nil
	}

	if secret == "" {
		generated, err := GenerateSessionSecret()
		if err != nil {
			return fmt.Errorf("config: generate SESSION_SECRET failed: %w", err)
		}
		c.Session.Secret = generated
		c.Session.Generated = true
		return nil
	}

	c.Session.Secret = secret
	return nil
}

// GenerateSessionSecret 生成 32 字节的随机十六进制会话密钥。
func GenerateSessionSecret() (string, error) {
	buf := make([]byte, SessionSecretMinLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
