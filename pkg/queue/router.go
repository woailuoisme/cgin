package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
)

// TaskHandler 描述消费端处理原始载荷的原子执行函数。
type TaskHandler func(ctx context.Context, payload []byte) error

// TaskRouter 显式任务多路复用路由器（对标 http.ServeMux，杜绝全局单例状态）。
type TaskRouter struct {
	mu        sync.RWMutex
	handlers  map[string]TaskHandler
	taskLocks map[string]time.Duration
}

// NewTaskRouter 创建一个全新的独立任务路由器实例。
func NewTaskRouter() *TaskRouter {
	return &TaskRouter{
		handlers:  make(map[string]TaskHandler),
		taskLocks: make(map[string]time.Duration),
	}
}

// Handle 注册指定任务名称的原始载荷处理器。
func (r *TaskRouter) Handle(taskName string, h TaskHandler) {
	if taskName == "" {
		panic("queue: task name cannot be empty")
	}
	if h == nil {
		panic("queue: task handler cannot be nil for " + taskName)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.handlers[taskName] = h
}

// Register 泛型强类型处理器注册：
// 自动完成：解包载荷反序列化为类型 P -> 执行强类型业务 Handler -> 错误传递。
func Register[P any](r *TaskRouter, taskName string, handle func(ctx context.Context, payload P) error, opts ...TaskOption) {
	if r == nil {
		panic("queue: router cannot be nil")
	}

	settings := BuildTaskSettings(opts...)
	if settings.WithoutOverlapping {
		r.SetLockTTL(taskName, settings.LockTTL)
	}

	r.Handle(taskName, func(ctx context.Context, rawPayload []byte) error {
		var payload P
		if err := json.Unmarshal(rawPayload, &payload); err != nil {
			return fmt.Errorf("queue: failed to unmarshal payload into %T for task %q: %w", payload, taskName, err)
		}
		return handle(ctx, payload)
	})
}

// SetLockTTL 在路由表中记录指定任务的防重叠锁超时时间。
func (r *TaskRouter) SetLockTTL(taskName string, ttl time.Duration) {
	if taskName == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.taskLocks[taskName] = ttl
}

// GetLockTTL 查询指定任务是否配置了防重叠分布式锁。
func (r *TaskRouter) GetLockTTL(taskName string) (time.Duration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ttl, ok := r.taskLocks[taskName]
	return ttl, ok
}

// GetHandler 获取已注册的任务执行函数。
func (r *TaskRouter) GetHandler(taskName string) (TaskHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[taskName]
	return h, ok
}

// Handlers 返回当前所有已挂载任务处理函数的只读拷贝。
func (r *TaskRouter) Handlers() map[string]TaskHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return maps.Clone(r.handlers)
}

// TaskNames 返回所有已注册任务名称的切片副本。
func (r *TaskRouter) TaskNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Collect(maps.Keys(r.handlers))
}
