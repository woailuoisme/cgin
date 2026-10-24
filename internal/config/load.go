package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

const dotenvPath = ".env"

// Load 从环境文件与操作系统环境变量装载并校验全部应用配置。
func Load() (*Config, error) {
	v := viper.New()
	v.SetConfigFile(dotenvPath)
	v.SetConfigType("env")

	setDefaults(v)
	bindEnvKeys(v)
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.Is(err, os.ErrNotExist) && !errors.As(err, &notFound) {
			return nil, fmt.Errorf("config: load %s failed: %w", dotenvPath, err)
		}
	}

	cfg := &Config{}
	decodeHook := viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		stringToSliceHookFunc(","),
	))
	if err := v.Unmarshal(cfg, decodeHook); err != nil {
		return nil, fmt.Errorf("config: parse failed: %w", err)
	}

	cfg.Database.Debug = cfg.ResolveDBDebug()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Default 返回一份最小可用默认配置，供配置装载失败时的路由自省类命令使用。
func Default() *Config {
	return &Config{
		App: AppConfig{
			Name: "bgin",
			Env:  "local",
		},
	}
}
