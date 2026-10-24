package queue

import (
	"context"

	"github.com/hibiken/asynq"

	"bgin/internal/config"
)

// Dispatcher 是业务代码可见的唯一入队入口。
//
// 契约：Job 自声明的执行约束（队列名、重试预算、执行超时、结果保留）由实现负责补齐；
// 调用方在 opts 中传入与自声明同名的选项时覆盖自声明 —— Asynq 的选项后写入者生效，
// 因此「本次派发需要特殊约束」由调用方决定，其余情况交给 Job 自己表达。
type Dispatcher interface {
	Dispatch(ctx context.Context, job Job, opts ...DispatchOption) error
}

// Inspector 是运维巡检面：队列状态与归档失败任务的跨队列与单队列重试、删除、清空。
//
// 返回值直接使用 Asynq 原生类型（asynq.TaskInfo），不做有损投影，
// 因此调用方能看到重试次数、重试上限与最后一次失败时间等完整上下文。
//
// 这些方法刻意不接收 context.Context：底层 asynq.Inspector 的全部接口都没有 ctx 形参，
// 无法传递取消与超时；为免制造"看起来支持取消"的假象，本层与之一致，
// 调用方需自行以超时包装或独立 goroutine 约束调用时长。
type Inspector interface {
	Queues() ([]QueueStatus, error)
	ListFailed(queue string, page, pageSize int) ([]*asynq.TaskInfo, error)
	ListFailedAll(page, pageSize int) ([]*asynq.TaskInfo, error)
	FindFailed(id string) (*asynq.TaskInfo, error)
	RetryFailed(queue, id string) error
	RetryFailedAll() (int, error)
	DeleteFailed(queue, id string) error
	DeleteFailedJob(id string) error
	ClearQueue(queue string) error
	PurgeArchived(queue string) error
	PurgeArchivedAll() error
}

// Worker 是进程生命周期面：启停 worker 与周期性调度器。
//
// 队列组成与优先级权重属于部署决策，因此由调用方以 队列名→权重 的形式传入，
// 而不是固化在模块内部；未声明任何队列时只监听默认队列。
type Worker interface {
	RunWorker(queues map[string]int) error
	StopWorker() error
	StartScheduler(cfg *config.Config) error
	RunScheduler(ctx context.Context, cfg *config.Config) error
	StopScheduler() error
}
