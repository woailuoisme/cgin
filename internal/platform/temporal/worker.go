package temporal

import (
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"bgin/internal/config"
)

// NewWorker 构造并配置指定任务队列的 Temporal Worker 实例。
func NewWorker(c client.Client, taskQueue string, cfg config.TemporalConfig) worker.Worker {
	return worker.New(c, taskQueue, worker.Options{
		MaxConcurrentActivityExecutionSize:     cfg.ActivityConcurrency,
		MaxConcurrentWorkflowTaskExecutionSize: cfg.WorkflowConcurrency,
	})
}
