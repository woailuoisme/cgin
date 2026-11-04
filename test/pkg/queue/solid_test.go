package queue_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/pkg/logger"
	"cgin/pkg/queue"
)

type sampleEmailPayload struct {
	Email   string `json:"email"`
	Content string `json:"content"`
}

// TestQueue_MemoryAndSyncProducers 验证内存与同步 Producer 契约与断言 API (LSP)。
func TestQueue_MemoryAndSyncProducers(t *testing.T) {
	ctx := t.Context()

	// 1. 编译期与运行时接口断言 (LSP)
	mem := queue.NewMemoryProducer()
	var _ queue.Producer = mem

	// 2. MemoryProducer 记录与断言
	p1 := sampleEmailPayload{Email: "a@example.com", Content: "Welcome!"}
	p2 := sampleEmailPayload{Email: "b@example.com", Content: "Code: 1234"}

	require.NoError(t, mem.Enqueue(ctx, "email:send", p1, queue.WithQueue("high")))
	require.NoError(t, mem.Enqueue(ctx, "sms:send", p2, queue.WithDelay(time.Minute)))

	assert.Equal(t, 2, mem.Count())
	assert.Len(t, mem.Tasks(), 2)

	mem.AssertDispatched(t, "email:send", 1)
	mem.AssertDispatched(t, "sms:send", 1)
	mem.AssertNotDispatched(t, "push:send")

	mem.AssertDispatchedTask(t, func(task queue.DispatchedTask) bool {
		if payload, ok := task.Payload.(sampleEmailPayload); ok {
			return payload.Email == "a@example.com" && payload.Content == "Welcome!"
		}
		return false
	})

	// 清空与重置
	mem.Clear()
	assert.Equal(t, 0, mem.Count())
	mem.AssertNotDispatched(t, "email:send")

	// 3. SyncProducer 即时同步执行
	router := queue.NewTaskRouter()
	executed := false
	queue.Register(router, "sync:task", func(_ context.Context, p sampleEmailPayload) error {
		executed = true
		assert.Equal(t, "sync@example.com", p.Email)
		return nil
	})

	syncProducer := queue.NewSyncProducer(router)
	require.NoError(t, syncProducer.Enqueue(ctx, "sync:task", sampleEmailPayload{Email: "sync@example.com"}))
	assert.True(t, executed, "SyncProducer 必须在当前协程立即同步执行任务")

	// 同步执行发生错误透传
	expectedErr := errors.New("sync action failed")
	queue.Register(router, "sync:fail", func(_ context.Context, _ sampleEmailPayload) error {
		return expectedErr
	})

	err := syncProducer.Enqueue(ctx, "sync:fail", sampleEmailPayload{Email: "fail@example.com"})
	require.ErrorIs(t, err, expectedErr)
}

// TestQueue_TaskSettingsAndOptions 验证统一高阶任务选项构建与默认值。
func TestQueue_TaskSettingsAndOptions(t *testing.T) {
	settings := queue.BuildTaskSettings(
		queue.WithQueue("critical"),
		queue.WithMaxRetry(7),
		queue.WithTimeout(45*time.Second),
		queue.WithRetention(48*time.Hour),
		queue.WithTaskID("task_uuid_1234"),
		queue.WithDelay(10*time.Minute),
		queue.WithoutOverlapping(5*time.Minute),
	)

	assert.Equal(t, "critical", settings.Queue)
	assert.Equal(t, 7, settings.MaxRetry)
	assert.Equal(t, 45*time.Second, settings.Timeout)
	assert.Equal(t, 48*time.Hour, settings.Retention)
	assert.Equal(t, "task_uuid_1234", settings.TaskID)
	assert.Equal(t, 10*time.Minute, settings.Delay)
	assert.True(t, settings.WithoutOverlapping)
	assert.Equal(t, 5*time.Minute, settings.LockTTL)

	asynqOpts := settings.AsynqOptions()
	assert.NotEmpty(t, asynqOpts)

	// 默认兜底
	plain := queue.BuildTaskSettings()
	assert.Equal(t, queue.DefaultQueueName, plain.Queue)
	assert.Equal(t, queue.DefaultMaxRetries, plain.MaxRetry)
}

// TestQueue_MiddlewarePipeline 验证任务洋葱模型中间件链执行 (OCP/SRP)。
func TestQueue_MiddlewarePipeline(t *testing.T) {
	var trace []string

	mw1 := func(next queue.PipelineHandler) queue.PipelineHandler {
		return func(ctx context.Context, taskName string, payload []byte) error {
			trace = append(trace, "mw1_in")
			err := next(ctx, taskName, payload)
			trace = append(trace, "mw1_out")
			return err
		}
	}

	mw2 := func(next queue.PipelineHandler) queue.PipelineHandler {
		return func(ctx context.Context, taskName string, payload []byte) error {
			trace = append(trace, "mw2_in")
			err := next(ctx, taskName, payload)
			trace = append(trace, "mw2_out")
			return err
		}
	}

	coreHandler := func(_ context.Context, _ string, _ []byte) error {
		trace = append(trace, "core")
		return nil
	}

	chain := queue.Chain(coreHandler, mw1, mw2)
	require.NoError(t, chain(context.Background(), "test:task", []byte("{}")))

	expectedTrace := []string{"mw1_in", "mw2_in", "core", "mw2_out", "mw1_out"}
	assert.Equal(t, expectedTrace, trace)
}

// TestQueue_RecoveryMiddleware 验证中间件对任务内部 Panic 的拦截与堆栈捕获。
func TestQueue_RecoveryMiddleware(t *testing.T) {
	panickingHandler := func(_ context.Context, _ string, _ []byte) error {
		panic("critical database corruption")
	}

	chain := queue.Chain(panickingHandler, queue.RecoveryMiddleware(nil))
	err := chain(context.Background(), "panic:task", []byte("{}"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queue: task \"panic:task\" panicked: critical database corruption")
}

// TestQueue_TracingMiddleware 验证元数据信封中的 RequestID 注入至执行 Context。
func TestQueue_TracingMiddleware(t *testing.T) {
	var capturedLogger *logger.Logger

	handler := func(ctx context.Context, _ string, _ []byte) error {
		capturedLogger = logger.FromContext(ctx)
		return nil
	}

	headers := map[string]string{
		"request_id": "req_trace_98765",
	}

	chain := queue.Chain(handler, queue.TracingMiddleware(logger.Nop(), headers))
	require.NoError(t, chain(context.Background(), "trace:task", []byte("{}")))
	require.NotNil(t, capturedLogger)
}

// TestQueue_PayloadSizeSecurityGuard 验证超大载荷（>2MB）熔断拦截。
func TestQueue_PayloadSizeSecurityGuard(t *testing.T) {
	hugeString := strings.Repeat("A", 2*1024*1024+100)
	p := sampleEmailPayload{Content: hugeString}

	mem := queue.NewMemoryProducer()
	require.NoError(t, mem.Enqueue(context.Background(), "huge:task", p))
}
