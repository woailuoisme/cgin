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
	Name       string           `mapstructure:"app_name"`
	Env        string           `mapstructure:"app_env"`
	Debug      bool             `mapstructure:"app_debug"`
	Port       int              `mapstructure:"app_port"`
	GinMode    string           `mapstructure:"gin_mode"`
	EnableDocs bool             `mapstructure:"enable_docs"`
	URL        string           `mapstructure:"app_url"`
	Timezone   string           `mapstructure:"app_timezone"`
	Server     HTTPServerConfig `mapstructure:",squash"`
}

// HTTPServerConfig 描述 HTTP 传输层超时与缓冲区预算。
type HTTPServerConfig struct {
	ReadHeaderTimeout time.Duration `mapstructure:"http_read_header_timeout"`
	ReadTimeout       time.Duration `mapstructure:"http_read_timeout"`
	WriteTimeout      time.Duration `mapstructure:"http_write_timeout"`
	IdleTimeout       time.Duration `mapstructure:"http_idle_timeout"`
	ShutdownTimeout   time.Duration `mapstructure:"http_shutdown_timeout"`
	MaxHeaderBytes    int           `mapstructure:"http_max_header_bytes"`
	RequestTimeout    time.Duration `mapstructure:"http_request_timeout"`
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
