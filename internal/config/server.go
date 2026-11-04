package config

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// gin 运行模式枚举值。
const (
	ModeDebug   = "debug"
	ModeRelease = "release"
	ModeTest    = "test"
)

// knownEnvs 声明 APP_ENV 合法环境名清单。
var knownEnvs = []string{"local", "development", "test", "staging", "production"}

// AppConfig 描述进程核心运行参数（对齐 Laravel config/app.php）。
type AppConfig struct {
	Name       string `env:"APP_NAME" envDefault:"cgin"`
	Env        string `env:"APP_ENV" envDefault:"local"`
	Debug      bool   `env:"APP_DEBUG"`
	Port       int    `env:"APP_PORT" envDefault:"3300"`
	GinMode    string `env:"GIN_MODE"`
	EnableDocs bool   `env:"ENABLE_DOCS"`
	URL        string `env:"APP_URL" envDefault:"http://localhost:3300"`
	Timezone   string `env:"APP_TIMEZONE" envDefault:"UTC"`
	Server     HTTPServerConfig
}

// Location 返回配置的时区 *time.Location，未设置或解析失败时回退至 time.UTC。
func (a *AppConfig) Location() *time.Location {
	if loc, err := time.LoadLocation(a.Timezone); err == nil {
		return loc
	}
	return time.UTC
}

// Validate 检查应用环境标识、时区与内嵌 HTTP 传输层配置合法性。
func (a *AppConfig) Validate() error {
	var appErr error
	if !slices.Contains(knownEnvs, a.Env) {
		appErr = fmt.Errorf("config: APP_ENV=%q is not a valid value, options: %s", a.Env, strings.Join(knownEnvs, " / "))
	} else if a.Timezone != "" {
		if _, err := time.LoadLocation(a.Timezone); err != nil {
			appErr = fmt.Errorf("config: APP_TIMEZONE=%q is not a valid timezone: %w", a.Timezone, err)
		}
	}
	return errors.Join(appErr, a.Server.Validate())
}

// ResolveGinMode 计算 gin 运行模式，优先显式 GIN_MODE，未设置时按环境推导。
func (c *Config) ResolveGinMode() string {
	if c.App.GinMode != "" {
		return c.App.GinMode
	}
	if c.IsLocal() {
		return ModeDebug
	}
	return ModeRelease
}

// IsLocal 报告是否处于本地开发环境。
func (c *Config) IsLocal() bool {
	return c.App.Env == "local" || c.App.Env == "development"
}

// IsProduction 报告是否处于生产环境。
func (c *Config) IsProduction() bool {
	return c.App.Env == "production"
}

// HTTPServerConfig 描述 HTTP 传输层超时与缓冲区预算。
type HTTPServerConfig struct {
	ReadHeaderTimeout time.Duration `env:"HTTP_READ_HEADER_TIMEOUT" envDefault:"10s"`
	ReadTimeout       time.Duration `env:"HTTP_READ_TIMEOUT" envDefault:"15s"`
	WriteTimeout      time.Duration `env:"HTTP_WRITE_TIMEOUT" envDefault:"35s"`
	IdleTimeout       time.Duration `env:"HTTP_IDLE_TIMEOUT" envDefault:"120s"`
	ShutdownTimeout   time.Duration `env:"HTTP_SHUTDOWN_TIMEOUT" envDefault:"3s"`
	MaxHeaderBytes    int           `env:"HTTP_MAX_HEADER_BYTES" envDefault:"1048576"`
	RequestTimeout    time.Duration `env:"HTTP_REQUEST_TIMEOUT" envDefault:"15s"`
}

// Validate 检查 HTTP 传输层超时与报头长度约束。
func (s *HTTPServerConfig) Validate() error {
	if s.ReadHeaderTimeout <= 0 || s.ReadTimeout <= 0 || s.WriteTimeout <= 0 || s.ShutdownTimeout <= 0 || s.RequestTimeout <= 0 {
		return errors.New("config: HTTP server timeouts must be positive")
	}
	if s.RequestTimeout >= s.WriteTimeout {
		return errors.New("config: HTTP_REQUEST_TIMEOUT must be less than HTTP_WRITE_TIMEOUT")
	}
	if s.MaxHeaderBytes <= 0 {
		return errors.New("config: HTTP_MAX_HEADER_BYTES must be positive")
	}
	return nil
}

// LogConfig 描述日志级别、输出通道及进程环境上下文。
type LogConfig struct {
	Level       string `env:"LOG_LEVEL" envDefault:"info"`
	Channel     string `env:"LOG_CHANNEL" envDefault:"console"`
	ServiceName string
	Env         string
}

// I18nConfig 描述多语言本地化配置。
type I18nConfig struct {
	DefaultLanguage string `env:"APP_LOCALE" envDefault:"en"`
}

// CORSConfig 描述跨域资源共享策略。未配置时不放行任何跨域来源。
type CORSConfig struct {
	AllowedOrigins []string `env:"CORS_ALLOWED_ORIGINS"`
}

// Validate 校验跨域来源白名单：仅接受通配符 * 或带 scheme 的来源，并在生产环境中拒绝通配符。
func (c *CORSConfig) Validate(isProd bool) error {
	if isProd && slices.Contains(c.AllowedOrigins, "*") {
		return errors.New("config: CORS_ALLOWED_ORIGINS cannot be \"*\" in production, list explicit origins instead")
	}

	for _, origin := range c.AllowedOrigins {
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

// LimiterConfig 描述请求速率限制配额（每分钟最大请求数，0 为不限制）。
type LimiterConfig struct {
	Enabled       *bool `env:"RATELIMIT_ENABLED" envDefault:"true"`
	Anonymous     int   `env:"RATELIMIT_ANONYMOUS" envDefault:"60"`
	Authenticated int   `env:"RATELIMIT_AUTHENTICATED" envDefault:"300"`
	Sensitive     int   `env:"RATELIMIT_SENSITIVE" envDefault:"10"`
}

// IsEnabled 报告是否全局激活请求速率限制保护。
func (l LimiterConfig) IsEnabled() bool {
	return l.Enabled == nil || *l.Enabled
}

// Validate 校验速率限制配额：0 表示显式关闭，负值属于非法配置。
func (l *LimiterConfig) Validate() error {
	for _, tier := range []struct {
		name  string
		value int
	}{
		{"RATELIMIT_ANONYMOUS", l.Anonymous},
		{"RATELIMIT_AUTHENTICATED", l.Authenticated},
		{"RATELIMIT_SENSITIVE", l.Sensitive},
	} {
		if tier.value < 0 {
			return fmt.Errorf("config: %s cannot be negative, use 0 to disable the tier explicitly", tier.name)
		}
	}
	return nil
}

// JWTConfig 描述双令牌签名与有效时长（对齐 Laravel config/jwt.php）。
type JWTConfig struct {
	Secret     string        `env:"JWT_SECRET,required,notEmpty"`
	Issuer     string        `env:"JWT_ISSUER" envDefault:"cgin"`
	AccessTTL  time.Duration `env:"JWT_TTL" envDefault:"15m"`
	RefreshTTL time.Duration `env:"JWT_REFRESH_TTL" envDefault:"720h"`
}

// Validate 检查令牌密钥长度与有效时长合理性。
func (j *JWTConfig) Validate() error {
	if len(j.Secret) < 32 {
		return fmt.Errorf("config: JWT_SECRET must be at least 32 characters, got %d", len(j.Secret))
	}
	if j.AccessTTL <= 0 || j.RefreshTTL <= 0 {
		return errors.New("config: JWT_TTL and JWT_REFRESH_TTL must be positive")
	}
	if j.RefreshTTL <= j.AccessTTL {
		return errors.New("config: JWT_REFRESH_TTL must be greater than JWT_TTL")
	}
	return nil
}

// AuthConfig 描述认证策略开关与一次性凭据寿命（对齐 Laravel config/auth.php）。
type AuthConfig struct {
	RequireEmailVerification bool          `env:"REQUIRE_EMAIL_VERIFICATION"`
	OTPTTL                   time.Duration `env:"OTP_TTL" envDefault:"10m"`
	OTPMaxAttempts           int           `env:"OTP_MAX_ATTEMPTS" envDefault:"5"`
	PasswordResetTTL         time.Duration `env:"PASSWORD_RESET_TTL" envDefault:"30m"`
}

// Validate 检查认证相关寿命参数与限制次数合法性。
func (a *AuthConfig) Validate() error {
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
