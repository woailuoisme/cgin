package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
)

// DispatchedTask 记录已派发任务的元数据与载荷快照。
type DispatchedTask struct {
	TaskName string
	Payload  any
	Settings TaskSettings
}

var (
	_ Producer = (*MemoryProducer)(nil)
	_ Producer = (*SyncProducer)(nil)
)

// MemoryProducer 专为单元测试打造的高性能内存任务收集器 (LSP)。
type MemoryProducer struct {
	mu    sync.RWMutex
	tasks []DispatchedTask
}

// NewMemoryProducer 构建一个全新的空内存生产者。
func NewMemoryProducer() *MemoryProducer {
	return &MemoryProducer{}
}

// NewMemoryDispatcher 为 NewMemoryProducer 的兼容别名。
func NewMemoryDispatcher() *MemoryProducer {
	return NewMemoryProducer()
}

// Enqueue 将任务记录到内存切片中，完全满足 Producer 契约。
func (m *MemoryProducer) Enqueue(_ context.Context, taskName string, payload any, opts ...TaskOption) error {
	if taskName == "" {
		return errors.New("queue: cannot enqueue task with empty name")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.tasks = append(m.tasks, DispatchedTask{
		TaskName: taskName,
		Payload:  payload,
		Settings: BuildTaskSettings(opts...),
	})
	return nil
}

// Close 满足 Producer 接口。
func (m *MemoryProducer) Close() error {
	return nil
}

// Tasks 获取所有已入队任务的切片副本。
func (m *MemoryProducer) Tasks() []DispatchedTask {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.tasks)
}

// Dispatched 返回指定任务名称的全部已派发任务切片。
func (m *MemoryProducer) Dispatched(taskName string) []DispatchedTask {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var matched []DispatchedTask
	for _, t := range m.tasks {
		if t.TaskName == taskName {
			matched = append(matched, t)
		}
	}
	return matched
}

// Count 返回已投递的任务总数。
func (m *MemoryProducer) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tasks)
}

// Clear 清空已记录的任务，便于测试用例重置状态。
func (m *MemoryProducer) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks = m.tasks[:0]
}

// AssertDispatched 断言指定任务名的任务曾被入队。
func (m *MemoryProducer) AssertDispatched(tb testing.TB, taskName string, expectedCount ...int) {
	tb.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()

	matched := 0
	for _, t := range m.tasks {
		if t.TaskName == taskName {
			matched++
		}
	}

	if len(expectedCount) > 0 {
		exp := expectedCount[0]
		if matched != exp {
			tb.Fatalf("expected task %q to be dispatched %d times, but got %d", taskName, exp, matched)
		}
		return
	}

	if matched == 0 {
		tb.Fatalf("expected task %q to be dispatched at least once, but it was never dispatched", taskName)
	}
}

// AssertNotDispatched 断言指定任务名的任务从未被入队。
func (m *MemoryProducer) AssertNotDispatched(tb testing.TB, taskName string) {
	tb.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()

	if slices.ContainsFunc(m.tasks, func(t DispatchedTask) bool { return t.TaskName == taskName }) {
		tb.Fatalf("expected task %q not to be dispatched, but found it in memory producer", taskName)
	}
}

// AssertDispatchedTask 依据自定义谓词函数匹配并断言已入队任务。
func (m *MemoryProducer) AssertDispatchedTask(tb testing.TB, match func(t DispatchedTask) bool) {
	tb.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !slices.ContainsFunc(m.tasks, match) {
		tb.Fatal("expected matching dispatched task in memory producer, but none found")
	}
}

// SyncProducer 在调用线程内立即同步执行任务（专用于轻量集成测试）。
type SyncProducer struct {
	router *TaskRouter
}

// NewSyncProducer 创建同步立即执行的 Producer。
func NewSyncProducer(router *TaskRouter) *SyncProducer {
	return &SyncProducer{router: router}
}

// NewSyncDispatcher 为 NewSyncProducer 的兼容别名。
func NewSyncDispatcher(router ...*TaskRouter) *SyncProducer {
	if len(router) > 0 && router[0] != nil {
		return NewSyncProducer(router[0])
	}
	return NewSyncProducer(NewTaskRouter())
}

// Enqueue 序列化载荷并在当前 goroutine 立即同步调用注册的 Handler。
func (s *SyncProducer) Enqueue(ctx context.Context, taskName string, payload any, _ ...TaskOption) error {
	if s == nil || s.router == nil {
		return errors.New("queue: sync producer router is nil")
	}

	h, ok := s.router.GetHandler(taskName)
	if !ok {
		return fmt.Errorf("queue: no handler registered for task %q", taskName)
	}

	var rawPayload []byte
	var err error
	if payload == nil {
		rawPayload = []byte("{}")
	} else if b, ok := payload.([]byte); ok {
		rawPayload = b
	} else {
		rawPayload, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("queue: failed to marshal sync payload for %q: %w", taskName, err)
		}
	}

	return h(ctx, rawPayload)
}

// Close 满足 Producer 接口。
func (s *SyncProducer) Close() error {
	return nil
}
