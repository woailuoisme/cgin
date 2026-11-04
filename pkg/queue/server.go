package queue

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/hibiken/asynq"

	"cgin/pkg/logger"
)

// Server 专职负责任务消费协程池、洋葱中间件执行管线与优雅停机 (SRP)。
type Server struct {
	redisOpt        asynq.RedisConnOpt
	concurrency     int
	shutdownTimeout time.Duration
	logger          *slog.Logger
	locker          Locker
	middlewares     []Middleware

	mu          sync.RWMutex
	asynqServer *asynq.Server
}

// ServerOptions 封装 Worker Server 的构建选项。
type ServerOptions struct {
	Concurrency     int
	ShutdownTimeout time.Duration
	Logger          *slog.Logger
	Locker          Locker
	Middlewares     []Middleware
}

// ServerOption 函数式选项配置。
type ServerOption func(*ServerOptions)

// WithServerConcurrency 指定工作协程并发数。
func WithServerConcurrency(concurrency int) ServerOption {
	return func(o *ServerOptions) {
		o.Concurrency = concurrency
	}
}

// WithServerShutdownTimeout 指定优雅停机等待超时时间。
func WithServerShutdownTimeout(d time.Duration) ServerOption {
	return func(o *ServerOptions) {
		o.ShutdownTimeout = d
	}
}

// WithServerLogger 注入工作端运行日志器。
func WithServerLogger(l *slog.Logger) ServerOption {
	return func(o *ServerOptions) {
		o.Logger = l
	}
}

// WithServerLocker 注入防并发重叠分布式锁实现。
func WithServerLocker(l Locker) ServerOption {
	return func(o *ServerOptions) {
		o.Locker = l
	}
}

// WithServerMiddlewares 附加自定义用户中间件链。
func WithServerMiddlewares(mws ...Middleware) ServerOption {
	return func(o *ServerOptions) {
		o.Middlewares = append(o.Middlewares, mws...)
	}
}

// NewServer 创建消费端 Server 实例。
func NewServer(cfg Config, opts ...ServerOption) (*Server, error) {
	norm := cfg.Normalized()
	redisOpt, err := ParseRedisURI(norm.RedisURL)
	if err != nil {
		return nil, err
	}

	opt := ServerOptions{
		Concurrency:     norm.Concurrency,
		ShutdownTimeout: norm.ShutdownTimeout,
	}
	for _, fn := range opts {
		if fn != nil {
			fn(&opt)
		}
	}

	return &Server{
		redisOpt:        redisOpt,
		concurrency:     opt.Concurrency,
		shutdownTimeout: opt.ShutdownTimeout,
		logger:          cmp.Or(opt.Logger, logger.Nop()),
		locker:          opt.Locker,
		middlewares:     opt.Middlewares,
	}, nil
}

// Run 启动任务消费守护进程并阻塞等待，直至收到停机信号或发生不可恢复错误。
func (s *Server) Run(queues map[string]int, router *TaskRouter) error {
	if s == nil {
		return errors.New("queue: server is nil")
	}
	if router == nil {
		return errors.New("queue: router cannot be nil")
	}

	if len(queues) == 0 {
		s.logger.Warn("no queue weights specified, listening on default queue only")
		queues = map[string]int{DefaultQueueName: 1}
	}

	mux := asynq.NewServeMux()
	handlers := router.Handlers()

	for name, h := range handlers {
		taskName := name
		handler := h

		mux.HandleFunc(taskName, func(ctx context.Context, t *asynq.Task) error {
			env, err := unpackEnvelope(t.Payload())
			if err != nil {
				return fmt.Errorf("queue: failed to unpack envelope for task %q: %w", taskName, err)
			}

			coreHandler := func(jobCtx context.Context, _ string, payload []byte) error {
				return handler(jobCtx, payload)
			}

			// 内置中间件链：Recovery -> Tracing -> Locking -> Logging
			mws := []Middleware{
				RecoveryMiddleware(s.logger),
				TracingMiddleware(s.logger, env.Headers),
				LockingMiddleware(s.locker, router, s.logger),
				LoggingMiddleware(s.logger),
			}

			if len(s.middlewares) > 0 {
				mws = append(mws, s.middlewares...)
			}

			pipeline := Chain(coreHandler, mws...)
			return pipeline(ctx, taskName, env.Payload)
		})
	}

	srv := asynq.NewServer(
		s.redisOpt,
		asynq.Config{
			Concurrency:     s.concurrency,
			Queues:          maps.Clone(queues),
			ShutdownTimeout: s.shutdownTimeout,
			ErrorHandler:    asynq.ErrorHandlerFunc(s.handleTaskError),
		},
	)

	s.mu.Lock()
	s.asynqServer = srv
	s.mu.Unlock()

	return srv.Run(mux)
}

// Shutdown 触发工作端优雅停机。
func (s *Server) Shutdown() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	srv := s.asynqServer
	s.asynqServer = nil
	s.mu.Unlock()

	if srv != nil {
		srv.Shutdown()
		s.logger.Info("queue worker server stopped successfully")
	}

	return nil
}

// handleTaskError 统一记录任务执行失败或耗尽重试错误。
func (s *Server) handleTaskError(ctx context.Context, task *asynq.Task, err error) {
	retried, hasRetried := asynq.GetRetryCount(ctx)
	maxRetry, hasMax := asynq.GetMaxRetry(ctx)
	taskID, _ := asynq.GetTaskID(ctx)
	queueName, _ := asynq.GetQueueName(ctx)

	attrs := []slog.Attr{
		slog.String("task_id", taskID),
		slog.String("task", task.Type()),
		slog.String("queue", queueName),
		slog.Any("error", err),
	}

	if hasRetried && hasMax {
		attrs = append(attrs, slog.Int("retried", retried), slog.Int("max_retry", maxRetry))
		if retried >= maxRetry {
			s.logger.LogAttrs(ctx, slog.LevelError, "task retries exhausted, moved to dead letter archive", attrs...)
			return
		}
	}

	s.logger.LogAttrs(ctx, slog.LevelError, "task execution failed, retry scheduled", attrs...)
}
