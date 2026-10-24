package queue

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"github.com/samber/lo"
	"go.uber.org/zap"

	"bgin/internal/config"
)

// RunWorker 启动后台工作协程服务，按队列权重消费任务。
// 队列组成属于部署决策，因此权重由调用方给出；未声明时只监听默认队列。
func (c *Client) RunWorker(queues map[string]int) error {
	if c == nil {
		return errors.New("queue: client is nil")
	}
	if len(queues) == 0 {
		c.log().Warn("no queue weights specified, worker listening on default queue only")
		queues = map[string]int{DefaultQueueName: 1}
	}

	srv := c.NewServer(queues)
	c.mu.Lock()
	c.server = srv
	c.mu.Unlock()

	return srv.Run(c.BuildServeMux())
}

// StopWorker 优雅停机并释放工作协程服务。
func (c *Client) StopWorker() error {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	srv := c.server
	c.server = nil
	c.mu.Unlock()

	if srv == nil {
		return nil
	}

	srv.Shutdown()

	return nil
}

// BuildServeMux 将注册表中的任务工厂编译挂载到 Asynq 的 ServeMux 多路复用器中。
// 每个任务执行前由工厂创建实例（元数据由构造器给出），再用载荷覆盖业务字段。
func (c *Client) BuildServeMux() *asynq.ServeMux {
	mux := asynq.NewServeMux()
	defaultReg.mu.RLock()
	defer defaultReg.mu.RUnlock()

	for name, factory := range defaultReg.jobs {
		taskName := name
		jobFactory := factory

		mux.HandleFunc(taskName, func(ctx context.Context, t *asynq.Task) error {
			job := jobFactory()
			if job == nil {
				return fmt.Errorf("queue: job factory returned nil instance for task %q", taskName)
			}

			headers, err := unwrapEnvelope(t.Payload(), job)
			if err != nil {
				return fmt.Errorf("queue: failed to unmarshal task payload for %q: %w", taskName, err)
			}

			coreHandler := func(jobCtx context.Context, j Job) error {
				return j.Handle(jobCtx)
			}

			// 内置中间件链：Recovery -> Tracing -> Locking -> Logging
			mws := []Middleware{
				RecoveryMiddleware(c.log()),
				TracingMiddleware(c.log(), headers),
				LockingMiddleware(c.locker, defaultReg, c.log()),
				LoggingMiddleware(c.log()),
			}

			// 附加全局用户自定义中间件
			c.mu.RLock()
			if len(c.middlewares) > 0 {
				mws = append(mws, c.middlewares...)
			}
			c.mu.RUnlock()

			pipeline := Chain(coreHandler, mws...)
			return pipeline(ctx, job)
		})
	}

	return mux
}

func (c *Client) newScheduler(cfg *config.Config) (*asynq.Scheduler, error) {
	if c == nil {
		return nil, errors.New("queue: client is nil")
	}

	loc := lo.Ternary(cfg != nil, cfg.App.Location(), time.UTC)

	scheduler := asynq.NewScheduler(c.redisOpt, &asynq.SchedulerOpts{
		Location: loc,
	})

	cronEntries := GetCronEntries()
	for _, entry := range cronEntries {
		if entry.Spec == "" || entry.Job == nil {
			continue
		}

		payload, err := json.Marshal(entry.Job)
		if err != nil {
			c.log().Error("failed to marshal periodic task payload",
				zap.String("task", entry.Job.TaskName()),
				zap.Error(err))
			continue
		}

		queueName := ResolveQueueName(entry.Job)
		task := asynq.NewTask(entry.Job.TaskName(), payload)
		opts := append([]asynq.Option{asynq.Queue(queueName)}, entry.Opts...)

		entryID, err := scheduler.Register(entry.Spec, task, opts...)
		if err != nil {
			c.log().Error("failed to register periodic task",
				zap.String("task", entry.Job.TaskName()),
				zap.String("spec", entry.Spec),
				zap.Error(err))
			continue
		}

		c.log().Info("periodic task registered successfully",
			zap.String("task", entry.Job.TaskName()),
			zap.String("spec", entry.Spec),
			zap.String("queue", queueName),
			zap.String("entry_id", entryID))
	}

	return scheduler, nil
}

// StartScheduler 异步启动周期性定时调度器（用于与 Worker 联合共存运行）。
func (c *Client) StartScheduler(cfg *config.Config) error {
	scheduler, err := c.newScheduler(cfg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.scheduler = scheduler
	c.mu.Unlock()

	go func() {
		if err := scheduler.Run(); err != nil {
			c.log().Error("periodic task scheduler encountered an error", zap.Error(err))
		}
	}()

	return nil
}

// RunScheduler 阻塞启动并运行周期性定时调度器，直到上下文被取消。
func (c *Client) RunScheduler(ctx context.Context, cfg *config.Config) error {
	scheduler, err := c.newScheduler(cfg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.scheduler = scheduler
	c.mu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		errCh <- scheduler.Run()
	}()

	select {
	case <-ctx.Done():
		_ = c.StopScheduler()
		return nil
	case err := <-errCh:
		c.mu.Lock()
		c.scheduler = nil
		c.mu.Unlock()
		return err
	}
}

// StopScheduler 优雅停止底层 asynq.Scheduler 周期调度器实例。
// 该方法是停机钩子，会在独立的信号 goroutine 中被调用，故与写入侧共用同一把锁。
func (c *Client) StopScheduler() error {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	scheduler := c.scheduler
	c.scheduler = nil
	c.mu.Unlock()

	if scheduler != nil {
		scheduler.Shutdown()
		c.log().Info("periodic task scheduler stopped")
	}

	return nil
}

// NewServer 创建用于承载任务执行的工作端 Asynq Server。
// 并发数来自 SetConcurrency，队列权重来自调用方；只有停机超时这类框架级策略留在模块内。
func (c *Client) NewServer(queues map[string]int) *asynq.Server {
	return asynq.NewServer(
		c.redisOpt,
		asynq.Config{
			Concurrency:     cmp.Or(c.Concurrency(), 10),
			Queues:          queues,
			ShutdownTimeout: c.ShutdownTimeout(),
			ErrorHandler:    asynq.ErrorHandlerFunc(c.handleTaskError),
		},
	)
}

// handleTaskError 统一拦截任务执行失败或耗尽重试事件。
func (c *Client) handleTaskError(ctx context.Context, task *asynq.Task, err error) {
	retried, hasRetried := asynq.GetRetryCount(ctx)
	maxRetry, hasMax := asynq.GetMaxRetry(ctx)
	taskID, _ := asynq.GetTaskID(ctx)
	queueName, _ := asynq.GetQueueName(ctx)

	fields := []zap.Field{
		zap.String("task_id", taskID),
		zap.String("task", task.Type()),
		zap.String("queue", queueName),
		zap.Error(err),
	}

	if hasRetried && hasMax {
		fields = append(fields, zap.Int("retried", retried), zap.Int("max_retry", maxRetry))
		if retried >= maxRetry {
			c.log().Error("task retries exhausted, moved to dead letter archive", fields...)
			return
		}
	}

	c.log().Error("task execution failed, retry scheduled", fields...)
}
