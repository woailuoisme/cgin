package queue

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/hibiken/asynq"
)

// RunWorker 启动后台工作协程服务，按队列权重消费任务。
// 队列组成属于部署决策，因此权重由调用方给出；未声明时只监听默认队列。
func (c *Client) RunWorker(queues map[string]int) error {
	if c == nil {
		return errors.New("queue: client is nil")
	}
	if len(queues) == 0 {
		c.log().Warn().Msg("no queue weights specified, worker listening on default queue only")
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
	reg := DefaultRegistry()
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	for name, factory := range reg.jobs {
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
				LockingMiddleware(c.locker, reg, c.log()),
				LoggingMiddleware(c.log()),
			}

			// 附加全局用户自定义中间件
			if len(c.middlewares) > 0 {
				mws = append(mws, c.middlewares...)
			}

			pipeline := Chain(coreHandler, mws...)
			return pipeline(ctx, job)
		})
	}

	return mux
}

func (c *Client) newScheduler(loc *time.Location) (*asynq.Scheduler, error) {
	if c == nil {
		return nil, errors.New("queue: client is nil")
	}
	if loc == nil {
		loc = time.UTC
	}

	scheduler := asynq.NewScheduler(c.redisOpt, &asynq.SchedulerOpts{
		Location: loc,
	})

	reg := DefaultRegistry()
	reg.mu.RLock()
	cronEntries := slices.Clone(reg.cronEntries)
	reg.mu.RUnlock()

	for _, entry := range cronEntries {
		if entry.Spec == "" || entry.Job == nil {
			continue
		}

		payload, err := wrapEnvelope(context.Background(), entry.Job)
		if err != nil {
			c.log().Error().
				Str("task", entry.Job.TaskName()).
				Err(err).
				Msg("failed to marshal periodic task payload")
			continue
		}

		task := asynq.NewTask(entry.Job.TaskName(), payload)
		// Job 自声明的重试/超时/保留与条目自定义选项全部下发（后者位置在后，同类型覆盖）。
		opts := append(JobOptions(entry.Job), entry.Opts...)

		entryID, err := scheduler.Register(entry.Spec, task, opts...)
		if err != nil {
			c.log().Error().
				Str("task", entry.Job.TaskName()).
				Str("spec", entry.Spec).
				Err(err).
				Msg("failed to register periodic task")
			continue
		}

		c.log().Info().
			Str("task", entry.Job.TaskName()).
			Str("spec", entry.Spec).
			Str("queue", ResolveQueueName(entry.Job)).
			Str("entry_id", entryID).
			Msg("periodic task registered successfully")
	}

	return scheduler, nil
}

// StartScheduler 启动周期性定时调度器，停机由 StopScheduler 统一负责。
//
// 刻意不使用 Scheduler.Run()：它在内部阻塞等待进程级 SIGTERM/SIGINT 并自行 Shutdown，
// 既与 queue:work 自身的信号处理重复注册，又会在被外部 Shutdown 后留下一个永不退出的
// 阻塞 goroutine 与一份泄漏的 signal 订阅。生命周期本层已显式掌控，故只调用非阻塞的 Start。
// 无已注册条目时不启动空转调度器，直接返回 nil。
//
// 注意：Start 返回后调度器在独立 goroutine 中运行，其中的 panic 无法被本函数 recover，
// 会直接终止进程——由进程守护负责重启，这是 asynq 的既定行为。
func (c *Client) StartScheduler(loc *time.Location) error {
	if len(DefaultRegistry().Entries()) == 0 {
		c.log().Debug().Msg("periodic task scheduler skipped, no entries registered")
		return nil
	}

	scheduler, err := c.newScheduler(loc)
	if err != nil {
		return err
	}
	if err := scheduler.Start(); err != nil {
		return err
	}

	c.mu.Lock()
	c.scheduler = scheduler
	c.mu.Unlock()

	return nil
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
		c.log().Info().Msg("periodic task scheduler stopped")
	}

	return nil
}

// NewServer 创建用于承载任务执行的工作端 Asynq Server。
// 并发数在构造期已固定，队列权重来自调用方；只有停机超时这类框架级策略留在模块内。
func (c *Client) NewServer(queues map[string]int) *asynq.Server {
	return asynq.NewServer(
		c.redisOpt,
		asynq.Config{
			Concurrency:     c.concurrency,
			Queues:          maps.Clone(queues),
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

	event := c.log().Error().
		Str("task_id", taskID).
		Str("task", task.Type()).
		Str("queue", queueName).
		Err(err)

	if hasRetried && hasMax {
		event = event.Int("retried", retried).Int("max_retry", maxRetry)
		if retried >= maxRetry {
			event.Msg("task retries exhausted, moved to dead letter archive")
			return
		}
	}

	event.Msg("task execution failed, retry scheduled")
}
