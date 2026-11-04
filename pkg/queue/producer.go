package queue

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"

	"cgin/pkg/errs"
	"cgin/pkg/logger"
)

// Producer 负责向队列安全投递任务的生产者契约 (SRP)。
type Producer interface {
	Enqueue(ctx context.Context, taskName string, payload any, opts ...TaskOption) error
	Close() error
}

// Dispatcher 为 Producer 的语义化别名，兼容分发器表达。
type Dispatcher = Producer

// AsynqProducer 基于 Asynq 的工业级队列任务生产者实现。
type AsynqProducer struct {
	client *asynq.Client
	logger *slog.Logger
}

var _ Producer = (*AsynqProducer)(nil)

// ProducerOptions 封装生产者构建时的函数式选项。
type ProducerOptions struct {
	Logger *slog.Logger
}

// ProducerOption 生产者选项配置函数。
type ProducerOption func(*ProducerOptions)

// WithProducerLogger 注入生产者专属的日志输出通道。
func WithProducerLogger(l *slog.Logger) ProducerOption {
	return func(o *ProducerOptions) {
		o.Logger = l
	}
}

// NewProducer 构建 AsynqProducer 实例。
func NewProducer(cfg Config, opts ...ProducerOption) (*AsynqProducer, error) {
	norm := cfg.Normalized()
	redisOpt, err := ParseRedisURI(norm.RedisURL)
	if err != nil {
		return nil, err
	}

	var opt ProducerOptions
	for _, fn := range opts {
		if fn != nil {
			fn(&opt)
		}
	}

	return &AsynqProducer{
		client: asynq.NewClient(redisOpt),
		logger: cmp.Or(opt.Logger, logger.Nop()),
	}, nil
}

// Enqueue 序列化任务载荷、包装元数据信封并派发至 Asynq 队列。
func (p *AsynqProducer) Enqueue(ctx context.Context, taskName string, payload any, opts ...TaskOption) error {
	if p == nil || p.client == nil {
		return errs.Internal(errors.New("queue: producer client is nil"))
	}
	if taskName == "" {
		return errs.Internal(errors.New("queue: task name cannot be empty"))
	}

	envBytes, err := packEnvelope(ctx, taskName, payload)
	if err != nil {
		return errs.Internal(err)
	}

	if len(envBytes) > MaxPayloadWarnBytes {
		p.logger.WarnContext(ctx, "task payload size exceeds 512KB recommendation",
			slog.String("task", taskName),
			slog.Int("size_bytes", len(envBytes)),
		)
	}

	settings := BuildTaskSettings(opts...)
	task := asynq.NewTask(taskName, envBytes)

	if _, err := p.client.EnqueueContext(ctx, task, settings.AsynqOptions()...); err != nil {
		return errs.Internal(fmt.Errorf("queue: failed to enqueue task %q: %w", taskName, err))
	}

	return nil
}

// Close 释放 Asynq 客户端底层连接池。
func (p *AsynqProducer) Close() error {
	if p == nil || p.client == nil {
		return nil
	}
	return p.client.Close()
}
