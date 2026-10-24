package config

import (
	"errors"
	"strings"
)

// CacheConfig 描述 Redis/Valkey 缓存连接参数（对齐 Laravel config/cache.php）。
type CacheConfig struct {
	URL string `mapstructure:"redis_url"`
}

// validateCache 检查缓存连接配置。
func (c *Config) validateCache() error {
	if strings.TrimSpace(c.Cache.URL) == "" {
		return errors.New("config: REDIS_URL is required")
	}
	return nil
}
