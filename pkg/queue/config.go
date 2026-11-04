package queue

import (
	"cmp"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
)

const (
	// DefaultQueueName 默认消费队列标识符。
	DefaultQueueName = "default"
	// DefaultConcurrency 默认工作协程并发数。
	DefaultConcurrency = 10
	// DefaultShutdownTimeout 默认优雅停机等待超时时间。
	DefaultShutdownTimeout = 3 * time.Second
)

// Config 封装队列平台基础设施的基础连接与运行时配置（保持平台层完全独立自洽）。
type Config struct {
	RedisURL        string
	Concurrency     int
	ShutdownTimeout time.Duration
}

// Normalized 返回填充了安全兜底值的配置副本。
func (c Config) Normalized() Config {
	return Config{
		RedisURL:        cmp.Or(c.RedisURL, "redis://127.0.0.1:6379"),
		Concurrency:     cmp.Or(c.Concurrency, DefaultConcurrency),
		ShutdownTimeout: cmp.Or(c.ShutdownTimeout, DefaultShutdownTimeout),
	}
}

// ParseRedisURI 解析 Redis 连接字串为 Asynq 驱动选项。
func ParseRedisURI(rawURL string) (asynq.RedisConnOpt, error) {
	redisURL := cmp.Or(rawURL, "redis://127.0.0.1:6379")
	opt, err := asynq.ParseRedisURI(redisURL)
	if err != nil {
		return nil, fmt.Errorf("queue: failed to parse redis uri %q: %w", redisURL, err)
	}
	return opt, nil
}
