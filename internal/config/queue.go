package config

import (
	"strings"
	"time"
)

// QueueConfig 描述异步队列与并发参数（对齐 Laravel config/queue.php）。
type QueueConfig struct {
	RedisURL        string        `env:"QUEUE_REDIS_URL"`
	Concurrency     int           `env:"QUEUE_CONCURRENCY" envDefault:"10"`
	ShutdownTimeout time.Duration `env:"QUEUE_SHUTDOWN_TIMEOUT" envDefault:"3s"`
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
