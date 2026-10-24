package config

import (
	"errors"
	"fmt"
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

// LogConfig 描述日志级别与输出通道（进程级设置，与 App 同属进程运行参数）。
type LogConfig struct {
	Level   string `env:"LOG_LEVEL" envDefault:"info"`
	Channel string `env:"LOG_CHANNEL" envDefault:"console"`
}

// I18nConfig 描述多语言本地化配置（进程级设置，与 App 同属进程运行参数）。
type I18nConfig struct {
	DefaultLanguage string `env:"APP_LOCALE" envDefault:"en"`
}

// Location 返回配置的时区 *time.Location，未设置或解析失败时回退至 time.UTC。
func (a *AppConfig) Location() *time.Location {
	if loc, err := time.LoadLocation(a.Timezone); err == nil {
		return loc
	}
	return time.UTC
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

// validateApp 检查应用环境标识与时区配置合法性。
func (c *Config) validateApp() error {
	if !slices.Contains(knownEnvs, c.App.Env) {
		return fmt.Errorf("config: APP_ENV=%q is not a valid value, options: %s", c.App.Env, strings.Join(knownEnvs, " / "))
	}
	if c.App.Timezone != "" {
		if _, err := time.LoadLocation(c.App.Timezone); err != nil {
			return fmt.Errorf("config: APP_TIMEZONE=%q is not a valid timezone: %w", c.App.Timezone, err)
		}
	}
	return nil
}

// validateServer 检查 HTTP 传输层超时与报头长度约束。
func (c *Config) validateServer() error {
	s := c.App.Server
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
