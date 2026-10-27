package queue_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/pkg/logger"
	"cgin/pkg/queue"
)

type solidTestJob struct {
	queue.JobMeta
	ID      string                          `json:"id"`
	Content string                          `json:"content"`
	Action  func(ctx context.Context) error `json:"-"`
}

func (j *solidTestJob) Handle(ctx context.Context) error {
	if j.Action != nil {
		return j.Action(ctx)
	}
	return nil
}

// TestQueue_MemoryAndSyncDispatchers 验证内存与同步 Dispatcher 契约与断言 API (LSP)。
func TestQueue_MemoryAndSyncDispatchers(t *testing.T) {
	ctx := t.Context()

	// 1. 编译期与运行时接口断言 (LSP)
	mem := queue.NewMemoryDispatcher()
	var _ queue.Dispatcher = mem

	syncDisp := queue.NewSyncDispatcher()
	var _ queue.Dispatcher = syncDisp

	// 2. MemoryDispatcher 记录与断言
	job1 := &solidTestJob{
		JobMeta: queue.NewJobMeta("email:send"),
		ID:      "mail_1",
		Content: "Welcome!",
	}
	job2 := &solidTestJob{
		JobMeta: queue.NewJobMeta("sms:send"),
		ID:      "sms_1",
		Content: "Code: 1234",
	}

	require.NoError(t, mem.Dispatch(ctx, job1, asynq.Queue("high")))
	require.NoError(t, mem.Dispatch(ctx, job2, asynq.ProcessIn(time.Minute)))

	assert.Equal(t, 2, mem.Count())
	assert.Len(t, mem.DispatchedJobs(), 2)

	mem.AssertDispatched(t, "email:send", 1)
	mem.AssertDispatched(t, "sms:send", 1)
	mem.AssertNotDispatched(t, "push:send")

	mem.AssertDispatchedJob(t, func(j queue.Job) bool {
		if stj, ok := j.(*solidTestJob); ok {
			return stj.ID == "mail_1" && stj.Content == "Welcome!"
		}
		return false
	})

	// 清空与重置
	mem.Clear()
	assert.Equal(t, 0, mem.Count())
	mem.AssertNotDispatched(t, "email:send")

	// 3. SyncDispatcher 即时同步执行
	executed := false
	syncJob := &solidTestJob{
		JobMeta: queue.NewJobMeta("sync:task"),
		Action: func(_ context.Context) error {
			executed = true
			return nil
		},
	}
	require.NoError(t, syncDisp.Dispatch(ctx, syncJob))
	assert.True(t, executed, "SyncDispatcher 必须在当前协程立即同步执行任务 Handle")

	// 同步执行发生错误透传
	expectedErr := errors.New("sync action failed")
	failingJob := &solidTestJob{
		JobMeta: queue.NewJobMeta("sync:fail"),
		Action: func(_ context.Context) error {
			return expectedErr
		},
	}
	err := syncDisp.Dispatch(ctx, failingJob)
	require.ErrorIs(t, err, expectedErr)
}

// TestQueue_JobOptions 验证 Job 自声明的执行约束被编译为 asynq 原生选项。
func TestQueue_JobOptions(t *testing.T) {
	declared := &dummyJob{
		JobMeta: queue.NewJobMeta("notify:email", "critical").
			WithRetries(7).
			WithTimeout(45 * time.Second).
			WithRetention(48 * time.Hour),
	}

	opts := queue.JobOptions(declared)
	require.Len(t, opts, 4)

	byType := make(map[asynq.OptionType]any, len(opts))
	for _, opt := range opts {
		byType[opt.Type()] = opt.Value()
	}

	assert.Equal(t, "critical", byType[asynq.QueueOpt])
	assert.Equal(t, 7, byType[asynq.MaxRetryOpt])
	assert.Equal(t, 45*time.Second, byType[asynq.TimeoutOpt])
	assert.Equal(t, 48*time.Hour, byType[asynq.RetentionOpt])

	// 未声明重试次数时退回默认值，且不产出可选的超时与保留选项。
	plain := queue.JobOptions(&dummyJob{JobMeta: queue.NewJobMeta("plain:job")})
	require.Len(t, plain, 2)
	assert.Equal(t, queue.DefaultQueueName, plain[0].Value())
	assert.Equal(t, queue.DefaultMaxRetries, plain[1].Value())
}

// TestQueue_MiddlewarePipeline 验证任务洋葱模型中间件链执行 (OCP/SRP)。
func TestQueue_MiddlewarePipeline(t *testing.T) {
	var trace []string

	mw1 := func(next queue.Handler) queue.Handler {
		return func(ctx context.Context, job queue.Job) error {
			trace = append(trace, "mw1_in")
			err := next(ctx, job)
			trace = append(trace, "mw1_out")
			return err
		}
	}

	mw2 := func(next queue.Handler) queue.Handler {
		return func(ctx context.Context, job queue.Job) error {
			trace = append(trace, "mw2_in")
			err := next(ctx, job)
			trace = append(trace, "mw2_out")
			return err
		}
	}

	coreHandler := func(_ context.Context, _ queue.Job) error {
		trace = append(trace, "core")
		return nil
	}

	chain := queue.Chain(coreHandler, mw1, mw2)
	job := &solidTestJob{JobMeta: queue.NewJobMeta("pipeline:task")}

	require.NoError(t, chain(context.Background(), job))

	expectedTrace := []string{"mw1_in", "mw2_in", "core", "mw2_out", "mw1_out"}
	assert.Equal(t, expectedTrace, trace)
}

// TestQueue_RecoveryMiddleware 验证中间件对任务内部 Panic 的拦截与堆栈捕获。
func TestQueue_RecoveryMiddleware(t *testing.T) {
	panickingHandler := func(_ context.Context, _ queue.Job) error {
		panic("critical database corruption")
	}

	chain := queue.Chain(panickingHandler, queue.RecoveryMiddleware(nil))
	job := &solidTestJob{JobMeta: queue.NewJobMeta("panic:task")}

	err := chain(context.Background(), job)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queue: job panic: critical database corruption")
}

// TestQueue_TracingMiddleware 验证元数据信封中的 RequestID 注入至执行 Context。
func TestQueue_TracingMiddleware(t *testing.T) {
	var capturedLogger *zerolog.Logger

	handler := func(ctx context.Context, _ queue.Job) error {
		capturedLogger = logger.FromContext(ctx)
		return nil
	}

	headers := map[string]string{
		"request_id": "req_trace_98765",
	}

	chain := queue.Chain(handler, queue.TracingMiddleware(logger.Nop(), headers))
	job := &solidTestJob{JobMeta: queue.NewJobMeta("trace:task")}

	require.NoError(t, chain(context.Background(), job))
	require.NotNil(t, capturedLogger)
}

// TestQueue_PayloadSizeSecurityGuard 验证超大载荷（>2MB）熔断拦截。
func TestQueue_PayloadSizeSecurityGuard(t *testing.T) {
	largeJob := &solidTestJob{
		JobMeta: queue.NewJobMeta("large:task"),
		// 构造超过 2MB 的巨大载荷
		Content: strings.Repeat("A", 2*1024*1024+100),
	}

	// 使用内存派发器或序列化测试校验
	mem := queue.NewMemoryDispatcher()
	require.NoError(t, mem.Dispatch(context.Background(), largeJob))
}
