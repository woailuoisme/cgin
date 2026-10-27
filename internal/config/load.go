package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"strings"

	"github.com/caarlos0/env/v11"
)

const dotenvPath = ".env"

// Load 从环境文件与操作系统环境变量装载并校验全部应用配置。
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

	cfg.Database.Debug = cfg.ResolveDBDebug()
	normalizeEmptySlices(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// normalizeEmptySlices 把 KEY= 解析出的单空元素切片还原为空切片。
// env 库对空字符串切片会切出一个空元素，语义上等同于"未配置"。
func normalizeEmptySlices(cfg *Config) {
	cfg.CORS.AllowedOrigins = dropBlankSingleton(cfg.CORS.AllowedOrigins)
	cfg.Media.AllowedExtensions = dropBlankSingleton(cfg.Media.AllowedExtensions)
	cfg.Media.AllowedMimeTypes = dropBlankSingleton(cfg.Media.AllowedMimeTypes)
	cfg.Payment.Wechat.Default.Scenes = dropBlankSingleton(cfg.Payment.Wechat.Default.Scenes)
	cfg.Payment.Alipay.Default.Scenes = dropBlankSingleton(cfg.Payment.Alipay.Default.Scenes)
}

// dropBlankSingleton 在切片仅含一个空白元素时返回 nil。
func dropBlankSingleton(values []string) []string {
	if len(values) == 1 && strings.TrimSpace(values[0]) == "" {
		return nil
	}
	return values
}

// Default 返回一份最小可用默认配置，供配置装载失败时的路由自省类命令使用。
func Default() *Config {
	return &Config{
		App: AppConfig{
			Name: "cgin",
			Env:  "local",
		},
		CORS: CORSConfig{
			AllowedOrigins: []string{"*"},
		},
	}
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
