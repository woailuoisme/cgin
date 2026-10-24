// Package async 提供轻量级有界缓冲 Goroutine Worker 池。
package async

import (
	"context"
	"errors"
	"runtime/debug"
	"sync"

	"go.uber.org/zap"

	"bgin/internal/platform/logger"
)

// 提交失败的原因：由调用方决定是降级同步执行还是丢弃，池自身不替调用方做取舍。
var (
	// ErrPoolClosed 表示任务池已关闭，不再接受新任务。
	ErrPoolClosed = errors.New("async pool is closed")
	// ErrQueueFull 表示缓冲队列已满，调用方应改为同步执行或显式丢弃。
	ErrQueueFull = errors.New("async pool queue is full")
)

// Task 表示在 Worker 池中执行的异步作业。
type Task func(ctx context.Context)

// Pool 管理固定数量的后台 Worker 并发处理任务。
type Pool struct {
	tasks chan Task
	wg    sync.WaitGroup
	once  sync.Once

	// mu 串行化「提交」与「关闭」：发送只在 RLock 下进行，关闭在 Lock 下置位并关闭队列，
	// 因此不存在向已关闭通道发送的窗口；closing 置位后提交一律被拒绝。
	mu      sync.RWMutex
	closing bool
}

// New 创建指定工作协程数和缓冲队列大小的任务池。
func New(workers, queueSize int) *Pool {
	if workers <= 0 {
		workers = 10
	}
	if queueSize <= 0 {
		queueSize = 100
	}

	p := &Pool{tasks: make(chan Task, queueSize)}

	p.wg.Add(workers)
	for range workers {
		go p.worker()
	}

	return p
}

// Submit 提交异步任务。
//
// 与「池内自行降级」相比，本方法把队列满与已关闭两种情况显式回传给调用方：
// 只有调用方知道该通知能否延迟、能否同步执行、能否丢弃。
// 池内既不起不受控的 goroutine，也不阻塞提交方的请求 goroutine。
//
// 传入的 ctx 只用于携带请求级元数据（日志器、追踪链路）：任务执行会被摘除取消信号，
// 因为异步任务的生命周期与触发它的请求无关，请求结束不应中断投递。
func (p *Pool) Submit(ctx context.Context, task Task) error {
	if task == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	baseCtx := context.WithoutCancel(ctx)

	// 用闭包承载基上下文与 panic 兜底，而不是把它们塞进队列元素的结构体里：
	// 上下文属于单次调用的作用域，不应成为跨 goroutine 传递的共享字段。
	// 形参刻意忽略：Task 的 ctx 由池在提交时刻派生（见上），执行期没有第二个上下文可选。
	job := func(_ context.Context) {
		defer recoverWithLog(baseCtx)
		task(baseCtx)
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closing {
		return ErrPoolClosed
	}

	select {
	case p.tasks <- job:
		return nil
	default:
		return ErrQueueFull
	}
}

// Close 优雅关闭任务池：先阻断新的提交，再关闭队列让 Worker 把已入队的任务排空后退出。
func (p *Pool) Close() {
	p.once.Do(func() {
		p.mu.Lock()
		p.closing = true
		// 此时所有发送方都已被 RLock 挡在外面，关闭队列不会与发送竞争。
		close(p.tasks)
		p.mu.Unlock()

		p.wg.Wait()
	})
}

// worker 消费任务队列，队列关闭即表示排空完成，随后退出。
func (p *Pool) worker() {
	defer p.wg.Done()
	for task := range p.tasks {
		task(context.Background())
	}
}

// recoverWithLog 捕获任务执行中的异常防止 Worker 崩溃，并用任务自身的上下文记录堆栈。
func recoverWithLog(ctx context.Context) {
	if r := recover(); r != nil {
		logger.FromContext(ctx).Error("panic recovered in async notification task execution",
			zap.Any("panic", r),
			zap.String("stack", string(debug.Stack())))
	}
}
