package queue

import (
	"time"

	"github.com/hibiken/asynq"
)

// DispatchConfig 汇总任务投递阶段的动态参数与执行约束。
type DispatchConfig struct {
	QueueName    string
	Delay        time.Duration
	ProcessAt    time.Time
	Timeout      time.Duration
	MaxRetries   int
	Retention    time.Duration
	UniqueTTL    time.Duration
	Headers      map[string]string
	RawAsynqOpts []asynq.Option
}

// DispatchOption 是任务投递选项的函数式配置契约 (DIP)。
// 解耦业务调用方对 hibiken/asynq 底层第三方库的直接依赖。
type DispatchOption func(*DispatchConfig)

// WithQueue 指定本次投递的目标队列名。
func WithQueue(name string) DispatchOption {
	return func(cfg *DispatchConfig) {
		cfg.QueueName = name
	}
}

// WithDelay 延迟指定时长后再将任务放入就绪队列。
func WithDelay(d time.Duration) DispatchOption {
	return func(cfg *DispatchConfig) {
		cfg.Delay = d
	}
}

// WithProcessAt 指定任务触发就绪的绝对时间点。
func WithProcessAt(t time.Time) DispatchOption {
	return func(cfg *DispatchConfig) {
		cfg.ProcessAt = t
	}
}

// WithTimeout 限制任务单次执行的最大超时时间。
func WithTimeout(d time.Duration) DispatchOption {
	return func(cfg *DispatchConfig) {
		cfg.Timeout = d
	}
}

// WithRetries 设置任务失败重试次数上限。
func WithRetries(n int) DispatchOption {
	return func(cfg *DispatchConfig) {
		cfg.MaxRetries = n
	}
}

// WithRetention 设置任务执行完成后在 Redis 中归档保留的时长。
func WithRetention(d time.Duration) DispatchOption {
	return func(cfg *DispatchConfig) {
		cfg.Retention = d
	}
}

// WithUnique 设置在指定 TTL 内防止相同任务重复入队的去重锁时长。
func WithUnique(ttl time.Duration) DispatchOption {
	return func(cfg *DispatchConfig) {
		cfg.UniqueTTL = ttl
	}
}

// WithHeader 为本次任务投递附加自定义元数据请求头（如 RequestID、TenantID）。
func WithHeader(key, value string) DispatchOption {
	return func(cfg *DispatchConfig) {
		if cfg.Headers == nil {
			cfg.Headers = make(map[string]string)
		}
		cfg.Headers[key] = value
	}
}

// WithAsynqOpt 兼容透传底层 Asynq 的原生选项，提供渐进式迁移能力。
func WithAsynqOpt(opts ...asynq.Option) DispatchOption {
	return func(cfg *DispatchConfig) {
		cfg.RawAsynqOpts = append(cfg.RawAsynqOpts, opts...)
	}
}

// compileAsynqOptions 将统一的 DispatchConfig 转换为底层 asynq.Option 序列。
func (c *DispatchConfig) compileAsynqOptions() []asynq.Option {
	opts := make([]asynq.Option, 0, len(c.RawAsynqOpts)+6)

	if c.QueueName != "" {
		opts = append(opts, asynq.Queue(c.QueueName))
	}
	if c.MaxRetries > 0 {
		opts = append(opts, asynq.MaxRetry(c.MaxRetries))
	}
	if c.Timeout > 0 {
		opts = append(opts, asynq.Timeout(c.Timeout))
	}
	if c.Retention > 0 {
		opts = append(opts, asynq.Retention(c.Retention))
	}
	if c.Delay > 0 {
		opts = append(opts, asynq.ProcessIn(c.Delay))
	} else if !c.ProcessAt.IsZero() {
		opts = append(opts, asynq.ProcessAt(c.ProcessAt))
	}
	if c.UniqueTTL > 0 {
		opts = append(opts, asynq.Unique(c.UniqueTTL))
	}

	opts = append(opts, c.RawAsynqOpts...)
	return opts
}
