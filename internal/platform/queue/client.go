package queue

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/samber/lo"
	"github.com/samber/oops"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
)

// Client 是队列基础设施的唯一实现类型：同一实例同时满足 Dispatcher、Inspector 与 Worker 三个 seam。
type Client struct {
	client          *asynq.Client
	inspector       *asynq.Inspector
	locker          Locker
	redisClient     redis.UniversalClient
	redisOpt        asynq.RedisConnOpt
	concurrency     int
	shutdownTimeout time.Duration
	logger          atomic.Pointer[zap.Logger]
	middlewares     []Middleware

	// mu 保护运行期可变字段：server 与 scheduler 由业务 goroutine 写入、停机钩子读取
	mu        sync.RWMutex
	server    *asynq.Server
	scheduler *asynq.Scheduler
}

var (
	_ Dispatcher = (*Client)(nil)
	_ Inspector  = (*Client)(nil)
	_ Worker     = (*Client)(nil)
)

// NewClient 初始化 Asynq 客户端与运维探针 Inspector。
// 优先使用 12-Factor 单行 URL（支持 TLS 与 Sentinel）。
func NewClient(cfg config.QueueConfig, lockers ...Locker) (*Client, func(), error) {
	redisURL := cmp.Or(cfg.RedisURL, "redis://127.0.0.1:6379")
	redisOpt, err := asynq.ParseRedisURI(redisURL)
	if err != nil {
		return nil, nil, oops.Code(string(errs.CodeInternal)).In("queue.NewClient.ParseRedisURI").Wrap(err)
	}

	client := asynq.NewClient(redisOpt)
	inspector := asynq.NewInspector(redisOpt)

	var locker Locker
	var rdb redis.UniversalClient
	if len(lockers) > 0 && lockers[0] != nil {
		locker = lockers[0]
	} else if rc, ok := redisOpt.MakeRedisClient().(redis.UniversalClient); ok {
		rdb = rc
		locker = NewRedisLocker(rc)
	}

	concurrency := cmp.Or(cfg.Concurrency, 10)
	shutdownTimeout := cmp.Or(cfg.ShutdownTimeout, 3*time.Second)

	c := &Client{
		client:          client,
		inspector:       inspector,
		locker:          locker,
		redisClient:     rdb,
		redisOpt:        redisOpt,
		concurrency:     concurrency,
		shutdownTimeout: shutdownTimeout,
	}

	cleanup := func() {
		_ = c.Close()
	}

	return c, cleanup, nil
}

// SetConcurrency 配置工作协程并发处理数上限。
func (c *Client) SetConcurrency(n int) {
	if n > 0 {
		c.mu.Lock()
		c.concurrency = n
		c.mu.Unlock()
	}
}

// Concurrency 返回配置的工作协程并发数上限。
func (c *Client) Concurrency() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.concurrency
}

// SetShutdownTimeout 设置优雅停机等待超时时间。
func (c *Client) SetShutdownTimeout(d time.Duration) {
	if d > 0 {
		c.mu.Lock()
		c.shutdownTimeout = d
		c.mu.Unlock()
	}
}

// ShutdownTimeout 返回优雅停机等待超时时间。
func (c *Client) ShutdownTimeout() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cmp.Or(c.shutdownTimeout, 10*time.Second)
}

// SetLogger 设置自定义日志记录器，无锁并发安全。
func (c *Client) SetLogger(l *zap.Logger) {
	c.logger.Store(l)
}

// fallbackLogger 是队列包内部告警（调度注册失败、锁续期失败等）的兜底输出。
// 默认静默：进程启动期应调用 SetFallbackLogger 注入真实日志器，否则这些告警无人可见。
var fallbackLogger atomic.Pointer[zap.Logger]

// SetFallbackLogger 注入队列包内部告警的兜底日志器（进程启动期调用一次）。
// 传 nil 复位为静默，便于测试隔离。
func SetFallbackLogger(l *zap.Logger) {
	fallbackLogger.Store(l)
}

// fallbackLog 返回生效的兜底日志器，未注入时为静默 Nop（永不为 nil）。
func fallbackLog() *zap.Logger {
	if l := fallbackLogger.Load(); l != nil {
		return l
	}
	return zap.NewNop()
}

// log 返回生效的日志器；未注入时退回包级兜底实例（永不为 nil，零锁竞争）。
func (c *Client) log() *zap.Logger {
	if c == nil {
		return fallbackLog()
	}
	if l := c.logger.Load(); l != nil {
		return l
	}
	return fallbackLog()
}

// Use 注册全局工作协程任务执行中间件 (OCP)。
func (c *Client) Use(middlewares ...Middleware) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.middlewares = append(c.middlewares, middlewares...)
}

// Close 释放队列客户端、运维探针与底层 Redis 资源。
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	if c.inspector != nil {
		_ = c.inspector.Close()
	}
	if c.client != nil {
		_ = c.client.Close()
	}
	if c.redisClient != nil {
		_ = c.redisClient.Close()
	}
	return nil
}

// Shutdown 优雅关闭队列客户端及其持有的所有连接资源。
func (c *Client) Shutdown() error {
	return c.Close()
}

// HealthCheck 检查队列底层 Redis 连接健康状态。
func (c *Client) HealthCheck() error {
	if c == nil || c.inspector == nil {
		return errors.New("queue: client is not initialized")
	}
	_, err := c.inspector.Queues()
	return err
}

// Dispatch 将任务投递到队列，自动序列化载荷并应用 Job 自声明的执行约束与元数据信封。
func (c *Client) Dispatch(ctx context.Context, job Job, opts ...DispatchOption) error {
	if c == nil || c.client == nil {
		return errs.Internal(errors.New("queue: client is nil"))
	}
	if job == nil {
		return errs.Internal(errors.New("queue: job is nil"))
	}

	cfg := &DispatchConfig{
		QueueName:  ResolveQueueName(job),
		MaxRetries: DefaultMaxRetries,
	}
	if rp, ok := job.(MaxRetriesProvider); ok && rp.MaxRetries() > 0 {
		cfg.MaxRetries = rp.MaxRetries()
	}
	if tp, ok := job.(TimeoutProvider); ok && tp.Timeout() > 0 {
		cfg.Timeout = tp.Timeout()
	}
	if retp, ok := job.(RetentionProvider); ok && retp.Retention() > 0 {
		cfg.Retention = retp.Retention()
	}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	payload, err := wrapEnvelope(ctx, job, cfg.Headers)
	if err != nil {
		return errs.Internal(err)
	}

	if len(payload) > MaxPayloadWarnBytes {
		c.log().Warn("task payload size exceeds 512KB recommendation",
			zap.String("task", job.TaskName()),
			zap.Int("size_bytes", len(payload)),
		)
	}

	task := asynq.NewTask(job.TaskName(), payload)
	asynqOpts := cfg.compileAsynqOptions()

	if _, err := c.client.EnqueueContext(ctx, task, asynqOpts...); err != nil {
		return errs.Internal(fmt.Errorf("queue: failed to enqueue task: %w", err))
	}

	return nil
}

// Queues 检查并返回当前所有队列的运行时健康状态与统计信息。
func (c *Client) Queues() ([]QueueStatus, error) {
	if c == nil || c.inspector == nil {
		return nil, errs.Internal(errors.New("queue: inspector is nil"))
	}

	queues, err := c.inspector.Queues()
	if err != nil {
		return nil, err
	}

	results := make([]QueueStatus, 0, len(queues))
	for _, q := range queues {
		info, err := c.inspector.GetQueueInfo(q)
		if err != nil {
			// 单个队列统计失败不阻断整体巡检，但必须留痕，否则运维只会看到"数据缺失"。
			c.log().Warn("failed to inspect queue info, skipped", zap.Error(err), zap.String("queue", q))
			continue
		}
		results = append(results, QueueStatus{
			Name:      q,
			Pending:   info.Pending,
			Running:   info.Active,
			Succeeded: info.Completed,
			Failed:    info.Archived,
			Scheduled: info.Scheduled,
			Retry:     info.Retry,
			Archived:  info.Archived,
			Paused:    info.Paused,
		})
	}

	return results, nil
}

// ListFailed 分页列出指定队列中已归档的失败任务，直接返回 Asynq 原生任务信息。
func (c *Client) ListFailed(queueName string, page, pageSize int) ([]*asynq.TaskInfo, error) {
	if c == nil || c.inspector == nil {
		return nil, errs.Internal(errors.New("queue: inspector is nil"))
	}
	queueName = cmp.Or(queueName, DefaultQueueName)
	page = max(page, 1)
	if pageSize <= 0 {
		pageSize = 100
	}

	tasks, err := c.inspector.ListArchivedTasks(queueName, asynq.Page(page), asynq.PageSize(pageSize))
	if errors.Is(err, asynq.ErrQueueNotFound) {
		// 队列尚未创建等价于「没有归档任务」，不应把空队列当成巡检失败。
		return nil, nil
	}

	return tasks, err
}

// ListFailedAll 分页获取所有活跃队列中已归档的失败任务（按最后失败时间倒序排列）。
func (c *Client) ListFailedAll(page, pageSize int) ([]*asynq.TaskInfo, error) {
	if c == nil || c.inspector == nil {
		return nil, errs.Internal(errors.New("queue: inspector is nil"))
	}
	page = max(page, 1)
	if pageSize <= 0 {
		pageSize = 100
	}

	allTasks, err := c.listAllArchivedTasks()
	if err != nil {
		return nil, err
	}

	slices.SortFunc(allTasks, func(a, b *asynq.TaskInfo) int {
		return b.LastFailedAt.Compare(a.LastFailedAt)
	})

	start := (page - 1) * pageSize
	if start >= len(allTasks) {
		return nil, nil
	}
	end := min(start+pageSize, len(allTasks))

	return allTasks[start:end], nil
}

// listAllArchivedTasks 遍历全部活跃队列汇总已归档的失败任务。
func (c *Client) listAllArchivedTasks() ([]*asynq.TaskInfo, error) {
	queues, err := c.inspector.Queues()
	if err != nil {
		return nil, err
	}

	var allTasks []*asynq.TaskInfo
	for _, q := range queues {
		tasks, err := c.inspector.ListArchivedTasks(q, asynq.Page(1), asynq.PageSize(1000))
		if err != nil {
			if !errors.Is(err, asynq.ErrQueueNotFound) {
				c.log().Warn("failed to list archived tasks, skipped", zap.Error(err), zap.String("queue", q))
			}
			continue
		}
		allTasks = append(allTasks, tasks...)
	}

	return allTasks, nil
}

// FindFailed 跨全部队列查找指定 ID 的归档失败任务。
func (c *Client) FindFailed(id string) (*asynq.TaskInfo, error) {
	if c == nil || c.inspector == nil {
		return nil, errs.Internal(errors.New("queue: inspector is nil"))
	}
	if id == "" {
		return nil, errs.NotFound("task id is empty")
	}

	allTasks, err := c.listAllArchivedTasks()
	if err != nil {
		return nil, err
	}

	if match, ok := lo.Find(allTasks, func(t *asynq.TaskInfo) bool { return t.ID == id }); ok {
		return match, nil
	}

	return nil, errs.NotFound("failed job " + id + " not found")
}

// RetryFailed 重新投递并执行指定的失败任务。
func (c *Client) RetryFailed(queueName, id string) error {
	if c == nil || c.inspector == nil {
		return errs.Internal(errors.New("queue: inspector is nil"))
	}
	return c.inspector.RunTask(cmp.Or(queueName, DefaultQueueName), id)
}

// RetryFailedAll 重试所有活跃队列中的全部归档失败任务，返回成功重新入队的数量。
func (c *Client) RetryFailedAll() (int, error) {
	if c == nil || c.inspector == nil {
		return 0, errs.Internal(errors.New("queue: inspector is nil"))
	}

	allTasks, err := c.listAllArchivedTasks()
	if err != nil {
		return 0, err
	}

	retriedCount := 0
	var failures []error

	for _, task := range allTasks {
		if err := c.inspector.RunTask(task.Queue, task.ID); err != nil {
			failures = append(failures, errs.Internal(fmt.Errorf("queue: failed to retry task %s: %w", task.ID, err)))
		} else {
			retriedCount++
		}
	}

	if len(failures) > 0 {
		return retriedCount, errors.Join(failures...)
	}
	return retriedCount, nil
}

// DeleteFailed 永久删除指定的归档失败任务。
func (c *Client) DeleteFailed(queueName, id string) error {
	if c == nil || c.inspector == nil {
		return errs.Internal(errors.New("queue: inspector is nil"))
	}
	return c.inspector.DeleteTask(cmp.Or(queueName, DefaultQueueName), id)
}

// DeleteFailedJob 跨全部队列查找并删除指定的归档失败任务。
func (c *Client) DeleteFailedJob(id string) error {
	task, err := c.FindFailed(id)
	if err != nil {
		return err
	}
	return c.inspector.DeleteTask(task.Queue, id)
}

// PurgeArchived 清空指定队列中已归档的失败任务。
func (c *Client) PurgeArchived(queueName string) error {
	if c == nil || c.inspector == nil {
		return errs.Internal(errors.New("queue: inspector is nil"))
	}
	_, err := c.inspector.DeleteAllArchivedTasks(cmp.Or(queueName, DefaultQueueName))
	return err
}

// PurgeArchivedAll 清空全部活跃队列中已归档的失败任务。
func (c *Client) PurgeArchivedAll() error {
	if c == nil || c.inspector == nil {
		return errs.Internal(errors.New("queue: inspector is nil"))
	}

	queues, err := c.inspector.Queues()
	if err != nil {
		return err
	}

	var failures []error
	for _, q := range queues {
		if _, err := c.inspector.DeleteAllArchivedTasks(q); err != nil {
			failures = append(failures, errs.Internal(fmt.Errorf("queue: failed to purge archived tasks from queue %s: %w", q, err)))
		}
	}

	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return nil
}

// ClearQueue 清空指定队列中尚未执行的待处理、已调度与重试中的任务。
// 与 PurgeArchived 的边界：这里删除的是**未执行**的任务，归档的失败记录由 PurgeArchived 负责。
func (c *Client) ClearQueue(queueName string) error {
	if c == nil || c.inspector == nil {
		return errs.Internal(errors.New("queue: inspector is nil"))
	}
	targetQueue := cmp.Or(queueName, DefaultQueueName)

	var failures []error
	for _, purge := range []struct {
		state  string
		delete func(string) (int, error)
	}{
		{state: "pending", delete: c.inspector.DeleteAllPendingTasks},
		{state: "scheduled", delete: c.inspector.DeleteAllScheduledTasks},
		{state: "retry", delete: c.inspector.DeleteAllRetryTasks},
	} {
		if _, err := purge.delete(targetQueue); err != nil {
			failures = append(failures, errs.Internal(fmt.Errorf("queue: failed to delete %s tasks: %w", purge.state, err)))
		}
	}

	return errors.Join(failures...)
}
