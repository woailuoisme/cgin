package queue

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
)

var (
	_ Dispatcher = (*MemoryDispatcher)(nil)
	_ Dispatcher = (*SyncDispatcher)(nil)
)

// MemoryDispatcher 是专为单元测试打造的内存任务收集器 (LSP)。
// 模拟队列入队并完整记录已派发的任务实例，无需启动本地 Redis。
type MemoryDispatcher struct {
	mu   sync.RWMutex
	jobs []Job
}

// NewMemoryDispatcher 构建一个全新的空内存派发器。
func NewMemoryDispatcher() *MemoryDispatcher {
	return &MemoryDispatcher{
		jobs: make([]Job, 0),
	}
}

// Dispatch 将任务记录到内部切片，完全满足 Dispatcher 契约。
func (m *MemoryDispatcher) Dispatch(_ context.Context, job Job, _ ...DispatchOption) error {
	if job == nil {
		return errors.New("queue: cannot dispatch nil job")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs = append(m.jobs, job)
	return nil
}

// DispatchedJobs 获取所有已入队任务的切片副本。
func (m *MemoryDispatcher) DispatchedJobs() []Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.jobs)
}

// Count 返回已投递的任务总数。
func (m *MemoryDispatcher) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.jobs)
}

// Clear 清空已记录的任务，便于在用例之间重置状态。
func (m *MemoryDispatcher) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs = m.jobs[:0]
}

// AssertDispatched 断言指定任务名的 Job 曾被入队。
// 可选参数 expectedCount 可精确断言入队次数。
func (m *MemoryDispatcher) AssertDispatched(tb testing.TB, taskName string, expectedCount ...int) {
	tb.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()

	matched := 0
	for _, j := range m.jobs {
		if j.TaskName() == taskName {
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

// AssertNotDispatched 断言指定任务名的 Job 从未被入队。
func (m *MemoryDispatcher) AssertNotDispatched(tb testing.TB, taskName string) {
	tb.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()

	if slices.ContainsFunc(m.jobs, func(j Job) bool { return j.TaskName() == taskName }) {
		tb.Fatalf("expected task %q not to be dispatched, but found it in memory queue", taskName)
	}
}

// AssertDispatchedJob 根据自定义谓词函数匹配并断言具体载荷字段。
func (m *MemoryDispatcher) AssertDispatchedJob(tb testing.TB, predicate func(j Job) bool) {
	tb.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !slices.ContainsFunc(m.jobs, predicate) {
		tb.Fatalf("expected matching job to be dispatched, but no matching job found")
	}
}

// SyncDispatcher 同步执行派发器：在 Dispatch 调用发生时立即在当前协程执行 job.Handle(ctx)。
// 适用于无需异步的本地 CLI 执行场景或事务提交即时验证测试。
type SyncDispatcher struct{}

// NewSyncDispatcher 创建一个同步派发器实例。
func NewSyncDispatcher() *SyncDispatcher {
	return &SyncDispatcher{}
}

// Dispatch 立即同步执行任务 Handle 方法。
func (s *SyncDispatcher) Dispatch(ctx context.Context, job Job, _ ...DispatchOption) error {
	if job == nil {
		return errors.New("queue: cannot dispatch nil job")
	}
	return job.Handle(ctx)
}
