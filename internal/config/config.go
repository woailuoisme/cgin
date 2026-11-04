// Package config 负责从环境变量装载、归一化并校验应用程序的全局配置。
package config

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"strings"

	"github.com/caarlos0/env/v11"
)

const (
	dotenvPath     = ".env"
	defaultAppName = "cgin"
)

// Config 汇总应用的所有配置模块。
type Config struct {
	App          AppConfig
	Database     DatabaseConfig
	Cache        CacheConfig
	Storage      StorageConfig
	JWT          JWTConfig
	Mail         MailConfig
	I18n         I18nConfig
	Log          LogConfig
	Auth         AuthConfig
	Limiter      LimiterConfig
	CORS         CORSConfig
	Seed         SeedConfig
	Queue        QueueConfig
	Search       SearchConfig
	Realtime     RealtimeConfig
	GeoIP        GeoIPConfig
	WeChat       WeChatConfig
	Payment      PaymentConfig
	Notification NotificationConfig
	Media        MediaConfig
	PDF          PDFConfig
	EventBus     EventBusConfig
	Telemetry    TelemetryConfig
}

// Load 按「解析 → 归一化 → 校验」三阶段流水线从环境文件与进程环境变量装载全局配置。
// 进程环境变量优先级高于 .env 文件，便于容器与 CI 覆盖本地配置。
func Load() (*Config, error) {
	environment, err := loadEnvironment()
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := env.ParseWithOptions(cfg, env.Options{Environment: environment}); err != nil {
		return nil, fmt.Errorf("config: parse failed: %w", err)
	}

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Normalize 集中执行跨模块默认值派生、空切片清洗与字符串裁剪，确保后续 Validate 保持纯函数无副作用。
func (c *Config) Normalize() {
	c.CORS.AllowedOrigins = dropBlankSingleton(c.CORS.AllowedOrigins)
	c.Database.Debug = c.Database.Debug || c.App.Debug
	c.Log.ServiceName = cmp.Or(c.Log.ServiceName, c.App.Name)
	c.Log.Env = cmp.Or(c.Log.Env, c.App.Env)
	c.Queue.Normalize(c.Cache.URL)
	c.EventBus.Normalize()
	c.Telemetry.Normalize(c.App.Name)
	c.Payment.Normalize(c.IsProduction())
	c.Notification.Normalize(c.IsProduction())
	c.Media.Normalize()
}

// Validate 依次执行各子配置模块的纯语义校验并汇总全部失败原因（errors.Join）。
func (c *Config) Validate() error {
	isProd := c.IsProduction()
	return errors.Join(
		c.App.Validate(),
		c.Database.Validate(),
		c.Cache.Validate(),
		c.Storage.Validate(),
		c.JWT.Validate(),
		c.Auth.Validate(),
		c.Mail.Validate(),
		c.Search.Validate(),
		c.Realtime.Validate(),
		c.CORS.Validate(isProd),
		c.Limiter.Validate(),
		c.WeChat.Validate(),
		c.Payment.Validate(),
		c.Notification.Validate(isProd),
		c.Telemetry.Validate(),
	)
}

// Default 返回一份最小可用默认配置，供配置装载失败时的路由自省类命令使用。
func Default() *Config {
	return &Config{
		App: AppConfig{
			Name: defaultAppName,
			Env:  "local",
		},
		CORS: CORSConfig{
			AllowedOrigins: []string{"*"},
		},
	}
}

// dropBlankSingleton 在切片仅含一个空白元素时返回 nil（env 库对 KEY= 空值会切出单空元素）。
func dropBlankSingleton(values []string) []string {
	if len(values) == 1 && strings.TrimSpace(values[0]) == "" {
		return nil
	}
	return values
}

// loadEnvironment 合并 .env 文件与进程环境变量，后者覆盖前者。
func loadEnvironment() (map[string]string, error) {
	environment, err := readDotEnv(dotenvPath)
	if err != nil {
		return nil, err
	}
	maps.Copy(environment, env.ToMap(os.Environ()))
	return environment, nil
}

// readDotEnv 逐行解析 KEY=VALUE 环境文件，支持成对引号与 export 前缀；不支持行内注释。
// 文件不存在视为没有本地覆盖；格式错误必须失败，避免静默退化为默认值。
func readDotEnv(path string) (map[string]string, error) {
	values := make(map[string]string)

	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return values, nil
		}
		return nil, fmt.Errorf("config: load %s failed: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		line = strings.TrimPrefix(line, "export ")
		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("config: load %s failed: line %d is not KEY=VALUE", path, lineNumber)
		}

		value, err := unquoteValue(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("config: load %s failed: line %d: %w", path, lineNumber, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("config: load %s failed: %w", path, err)
	}
	return values, nil
}

// unquoteValue 去除成对引号；引号未闭合时报错而不是原样保留。
func unquoteValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	switch raw[0] {
	case '"':
		if len(raw) < 2 || raw[len(raw)-1] != '"' {
			return "", errors.New("unterminated double quote")
		}
		return raw[1 : len(raw)-1], nil
	case '\'':
		if len(raw) < 2 || raw[len(raw)-1] != '\'' {
			return "", errors.New("unterminated single quote")
		}
		return raw[1 : len(raw)-1], nil
	default:
		return raw, nil
	}
}
