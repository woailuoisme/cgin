package config

import (
	"errors"
	"time"
)

// AuthConfig 描述认证策略开关与一次性凭据寿命（对齐 Laravel config/auth.php）。
type AuthConfig struct {
	RequireEmailVerification bool          `mapstructure:"require_email_verification"`
	OTPTTL                   time.Duration `mapstructure:"otp_ttl"`
	OTPMaxAttempts           int           `mapstructure:"otp_max_attempts"`
	PasswordResetTTL         time.Duration `mapstructure:"password_reset_ttl"`
}

// validateAuth 检查认证相关寿命参数与限制次数合法性。
func (c *Config) validateAuth() error {
	a := c.Auth
	if a.OTPTTL <= 0 {
		return errors.New("config: OTP_TTL must be positive")
	}
	if a.OTPMaxAttempts <= 0 {
		return errors.New("config: OTP_MAX_ATTEMPTS must be positive")
	}
	if a.PasswordResetTTL <= 0 {
		return errors.New("config: PASSWORD_RESET_TTL must be positive")
	}
	return nil
}
