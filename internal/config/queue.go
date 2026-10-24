package config

import (
	"strings"
	"time"
)

// QueueConfig 描述异步队列与并发参数（对齐 Laravel config/queue.php）。
type QueueConfig struct {
	RedisURL        string        `mapstructure:"queue_redis_url"`
	Concurrency     int           `mapstructure:"queue_concurrency"`
	ShutdownTimeout time.Duration `mapstructure:"queue_shutdown_timeout"`
}

// validateQueue 检查队列配置，缺省 Redis URL 时自动回退至全局缓存 Redis。
func (c *Config) validateQueue() error {
	if strings.TrimSpace(c.Queue.RedisURL) == "" {
		c.Queue.RedisURL = c.Cache.URL
	}
	if c.Queue.Concurrency <= 0 {
		c.Queue.Concurrency = 10
	}
	if c.Queue.ShutdownTimeout <= 0 {
		c.Queue.ShutdownTimeout = 3 * time.Second
	}
	return nil
}
