package queue

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/hibiken/asynq"

	"cgin/pkg/errs"
	"cgin/pkg/logger"
)

// QueueStatus 表示单个队列当前的运行统计指标。
type QueueStatus struct {
	Name      string
	Pending   int
	Running   int
	Succeeded int
	Scheduled int
	Retry     int
	Archived  int
	Paused    bool
}

// Inspector 专职负责队列监控、任务状态检索、死信重试与清空等运维契约 (SRP)。
type Inspector interface {
	Queues() ([]QueueStatus, error)
	ListFailed(queueName string, page, pageSize int) ([]*asynq.TaskInfo, error)
	ListFailedAll(page, pageSize int) ([]*asynq.TaskInfo, error)
	FindFailed(id string) (*asynq.TaskInfo, error)
	RetryFailed(queueName, id string) error
	RetryFailedAll() (int, error)
	DeleteFailed(queueName, id string) error
	DeleteFailedJob(id string) error
	ClearQueue(queueName string) error
	PurgeArchived(queueName string) error
	PurgeArchivedAll() error
	Close() error
}

// AsynqInspector 基于 Asynq 的工业级运维巡检实现。
type AsynqInspector struct {
	inspector *asynq.Inspector
	logger    *slog.Logger
}

var _ Inspector = (*AsynqInspector)(nil)

// InspectorOptions 封装巡检探针的可选参数。
type InspectorOptions struct {
	Logger *slog.Logger
}

// InspectorOption 巡检选项配置函数。
type InspectorOption func(*InspectorOptions)

// WithInspectorLogger 注入专属日志器。
func WithInspectorLogger(l *slog.Logger) InspectorOption {
	return func(o *InspectorOptions) {
		o.Logger = l
	}
}

// NewInspector 构建 AsynqInspector 实例。
func NewInspector(cfg Config, opts ...InspectorOption) (*AsynqInspector, error) {
	norm := cfg.Normalized()
	redisOpt, err := ParseRedisURI(norm.RedisURL)
	if err != nil {
		return nil, err
	}

	var opt InspectorOptions
	for _, fn := range opts {
		if fn != nil {
			fn(&opt)
		}
	}

	return &AsynqInspector{
		inspector: asynq.NewInspector(redisOpt),
		logger:    cmp.Or(opt.Logger, logger.Nop()),
	}, nil
}

// Close 释放 Asynq Inspector 探针资源。
func (i *AsynqInspector) Close() error {
	if i == nil || i.inspector == nil {
		return nil
	}
	return i.inspector.Close()
}

// Queues 检查并返回当前所有队列的健康状态与任务数统计。
func (i *AsynqInspector) Queues() ([]QueueStatus, error) {
	if i == nil || i.inspector == nil {
		return nil, errs.Internal(errors.New("queue: inspector is nil"))
	}

	queues, err := i.inspector.Queues()
	if err != nil {
		return nil, err
	}

	results := make([]QueueStatus, 0, len(queues))
	for _, q := range queues {
		info, err := i.inspector.GetQueueInfo(q)
		if err != nil {
			i.logger.Warn("failed to inspect queue info, skipped",
				slog.Any("error", err),
				slog.String("queue", q),
			)
			continue
		}
		results = append(results, QueueStatus{
			Name:      q,
			Pending:   info.Pending,
			Running:   info.Active,
			Succeeded: info.Completed,
			Scheduled: info.Scheduled,
			Retry:     info.Retry,
			Archived:  info.Archived,
			Paused:    info.Paused,
		})
	}

	return results, nil
}

// ListFailed 分页获取指定队列中归档的失败任务。
func (i *AsynqInspector) ListFailed(queueName string, page, pageSize int) ([]*asynq.TaskInfo, error) {
	if i == nil || i.inspector == nil {
		return nil, errs.Internal(errors.New("queue: inspector is nil"))
	}
	targetQueue := cmp.Or(queueName, DefaultQueueName)
	page = max(page, 1)
	if pageSize <= 0 {
		pageSize = 100
	}

	tasks, err := i.inspector.ListArchivedTasks(targetQueue, asynq.Page(page), asynq.PageSize(pageSize))
	if errors.Is(err, asynq.ErrQueueNotFound) {
		return nil, nil
	}

	return tasks, err
}

// ListFailedAll 跨所有活跃队列分页获取归档失败任务（按最后失败时间倒序排列）。
func (i *AsynqInspector) ListFailedAll(page, pageSize int) ([]*asynq.TaskInfo, error) {
	if i == nil || i.inspector == nil {
		return nil, errs.Internal(errors.New("queue: inspector is nil"))
	}
	page = max(page, 1)
	if pageSize <= 0 {
		pageSize = 100
	}

	allTasks, err := i.listAllArchivedTasks()
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

func (i *AsynqInspector) listAllArchivedTasks() ([]*asynq.TaskInfo, error) {
	queues, err := i.inspector.Queues()
	if err != nil {
		return nil, err
	}

	var allTasks []*asynq.TaskInfo
	for _, q := range queues {
		for page := 1; ; page++ {
			tasks, err := i.inspector.ListArchivedTasks(q, asynq.Page(page), asynq.PageSize(1000))
			if err != nil {
				if !errors.Is(err, asynq.ErrQueueNotFound) {
					i.logger.Warn("failed to list archived tasks, skipped",
						slog.Any("error", err),
						slog.String("queue", q),
					)
				}
				break
			}
			allTasks = append(allTasks, tasks...)
			if len(tasks) < 1000 {
				break
			}
		}
	}

	return allTasks, nil
}

// FindFailed 跨全部队列按 ID 查找指定的归档失败任务。
func (i *AsynqInspector) FindFailed(id string) (*asynq.TaskInfo, error) {
	if i == nil || i.inspector == nil {
		return nil, errs.Internal(errors.New("queue: inspector is nil"))
	}
	if id == "" {
		return nil, errs.NotFound("task id is empty")
	}

	allTasks, err := i.listAllArchivedTasks()
	if err != nil {
		return nil, err
	}

	idx := slices.IndexFunc(allTasks, func(t *asynq.TaskInfo) bool { return t.ID == id })
	if idx >= 0 {
		return allTasks[idx], nil
	}

	return nil, errs.NotFound("failed job " + id + " not found")
}

// RetryFailed 重新投递并执行指定的失败任务。
func (i *AsynqInspector) RetryFailed(queueName, id string) error {
	if i == nil || i.inspector == nil {
		return errs.Internal(errors.New("queue: inspector is nil"))
	}
	return i.inspector.RunTask(cmp.Or(queueName, DefaultQueueName), id)
}

// RetryFailedAll 重试所有活跃队列中的全部归档失败任务，返回成功重试计数。
func (i *AsynqInspector) RetryFailedAll() (int, error) {
	if i == nil || i.inspector == nil {
		return 0, errs.Internal(errors.New("queue: inspector is nil"))
	}

	allTasks, err := i.listAllArchivedTasks()
	if err != nil {
		return 0, err
	}

	retriedCount := 0
	var failures []error

	for _, task := range allTasks {
		if err := i.inspector.RunTask(task.Queue, task.ID); err != nil {
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
func (i *AsynqInspector) DeleteFailed(queueName, id string) error {
	if i == nil || i.inspector == nil {
		return errs.Internal(errors.New("queue: inspector is nil"))
	}
	return i.inspector.DeleteTask(cmp.Or(queueName, DefaultQueueName), id)
}

// DeleteFailedJob 跨全部队列查找并删除指定的归档失败任务。
func (i *AsynqInspector) DeleteFailedJob(id string) error {
	task, err := i.FindFailed(id)
	if err != nil {
		return err
	}
	return i.inspector.DeleteTask(task.Queue, id)
}

// PurgeArchived 清空指定队列中已归档的失败任务。
func (i *AsynqInspector) PurgeArchived(queueName string) error {
	if i == nil || i.inspector == nil {
		return errs.Internal(errors.New("queue: inspector is nil"))
	}
	_, err := i.inspector.DeleteAllArchivedTasks(cmp.Or(queueName, DefaultQueueName))
	return err
}

// PurgeArchivedAll 清空全部活跃队列中已归档的失败任务。
func (i *AsynqInspector) PurgeArchivedAll() error {
	if i == nil || i.inspector == nil {
		return errs.Internal(errors.New("queue: inspector is nil"))
	}

	queues, err := i.inspector.Queues()
	if err != nil {
		return err
	}

	var failures []error
	for _, q := range queues {
		if _, err := i.inspector.DeleteAllArchivedTasks(q); err != nil {
			failures = append(failures, errs.Internal(fmt.Errorf("queue: failed to purge archived tasks from queue %s: %w", q, err)))
		}
	}

	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return nil
}

// ClearQueue 清空指定队列中尚未执行的待处理、已调度与重试中的任务。
func (i *AsynqInspector) ClearQueue(queueName string) error {
	if i == nil || i.inspector == nil {
		return errs.Internal(errors.New("queue: inspector is nil"))
	}
	targetQueue := cmp.Or(queueName, DefaultQueueName)

	var failures []error
	for _, purge := range []struct {
		state  string
		delete func(string) (int, error)
	}{
		{state: "pending", delete: i.inspector.DeleteAllPendingTasks},
		{state: "scheduled", delete: i.inspector.DeleteAllScheduledTasks},
		{state: "retry", delete: i.inspector.DeleteAllRetryTasks},
	} {
		if _, err := purge.delete(targetQueue); err != nil {
			failures = append(failures, errs.Internal(fmt.Errorf("queue: failed to delete %s tasks: %w", purge.state, err)))
		}
	}

	return errors.Join(failures...)
}
