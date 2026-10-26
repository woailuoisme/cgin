// 本文件锁定会话 Cookie 的配置契约：生产环境绝不允许缺省密钥静默启动，
// 非生产环境缺省时必须自动生成一次性密钥并显式标记，避免弱密钥悄悄进入运行态。
package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
)

// newSessionConfig 构造只关心会话模块的配置，其余模块的校验噪声不参与断言。
func newSessionConfig(env string, session config.SessionConfig) *config.Config {
	return &config.Config{
		App:     config.AppConfig{Env: env},
		Session: session,
	}
}

// TestSessionSecretRequiredInProduction 生产环境必须显式配置足够强度的会话密钥。
func TestSessionSecretRequiredInProduction(t *testing.T) {
	tests := []struct {
		name    string
		secret  string
		wantMsg string
	}{
		{name: "缺失密钥", secret: "", wantMsg: "SESSION_SECRET is required in production"},
		{name: "密钥过短", secret: "short-secret", wantMsg: "SESSION_SECRET must be at least 32 characters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newSessionConfig("production", config.SessionConfig{
				Name:   "cgin_session",
				Secret: tt.secret,
				MaxAge: time.Hour,
			})

			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
			assert.False(t, cfg.Session.Generated, "生产环境不允许静默生成密钥")
		})
	}
}

// TestSessionSecretAcceptedInProduction 生产环境配置合规密钥后不再报告会话相关错误。
func TestSessionSecretAcceptedInProduction(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	cfg := newSessionConfig("production", config.SessionConfig{
		Name:   "cgin_session",
		Secret: secret,
		MaxAge: time.Hour,
	})

	err := cfg.Validate()
	if err != nil {
		assert.NotContains(t, err.Error(), "SESSION_SECRET")
	}
	assert.Equal(t, secret, cfg.Session.Secret)
	assert.False(t, cfg.Session.Generated)
}

// TestSessionSecretGeneratedOutsideProduction 非生产环境缺省密钥时自动生成并标记，供装配层告警。
func TestSessionSecretGeneratedOutsideProduction(t *testing.T) {
	cfg := newSessionConfig("local", config.SessionConfig{Name: "cgin_session", MaxAge: time.Hour})

	err := cfg.Validate()
	if err != nil {
		assert.NotContains(t, err.Error(), "SESSION_SECRET")
	}
	assert.True(t, cfg.Session.Generated, "自动生成的密钥必须打上标记，装配层据此输出告警")
	assert.Len(t, cfg.Session.Secret, config.SessionSecretMinLength*2, "密钥应为 32 字节的十六进制串")
}

// TestSessionRejectsInvalidNameAndMaxAge 会话名称不得为空，有效期不得为负。
func TestSessionRejectsInvalidNameAndMaxAge(t *testing.T) {
	t.Run("名称缺失", func(t *testing.T) {
		cfg := newSessionConfig("local", config.SessionConfig{MaxAge: time.Hour})
		require.Error(t, cfg.Validate())
		assert.Contains(t, cfg.Validate().Error(), "SESSION_NAME")
	})

	t.Run("有效期为负", func(t *testing.T) {
		cfg := newSessionConfig("local", config.SessionConfig{Name: "cgin_session", MaxAge: -time.Minute})
		require.Error(t, cfg.Validate())
		assert.Contains(t, cfg.Validate().Error(), "SESSION_MAX_AGE")
	})

	t.Run("有效期缺省时回填", func(t *testing.T) {
		cfg := newSessionConfig("local", config.SessionConfig{Name: "cgin_session"})
		_ = cfg.Validate()
		assert.Equal(t, 168*time.Hour, cfg.Session.MaxAge)
	})
}
