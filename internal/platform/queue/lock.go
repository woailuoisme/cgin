package queue

import (
	"context"
	"errors"
	"time"

	"cgin/internal/platform/cache"
)

// Locker 定义分布式互斥锁契约。
type Locker interface {
	// Acquire 尝试以非阻塞方式获取指定 key 的分布式锁。
	// 获取成功时返回释放函数和 acquired=true；已被占用时返回 acquired=false, err=nil。
	Acquire(ctx context.Context, key string, ttl time.Duration) (release func(context.Context) error, acquired bool, err error)
}

// TryLocker 抽象支持非阻塞 TryLock 契约的互斥锁管理器（符合 ISP 接口隔离原则，可直接对接 cache.Locker）。
type TryLocker interface {
	TryLock(ctx context.Context, name string) (context.Context, context.CancelFunc, error)
}

// AdaptLocker 将实现了 TryLocker 契约的互斥锁（如 cache.Locker）适配为 queue.Locker。
// 借助底层 Watchdog 看门狗自动续期能力，可彻底避免长时间任务执行期间锁意外过期导致的并发重叠。
func AdaptLocker(l TryLocker) Locker {
	if l == nil {
		return nil
	}
	return &tryLockerAdapter{l: l}
}

type tryLockerAdapter struct {
	l TryLocker
}

var _ Locker = (*tryLockerAdapter)(nil)

func (a *tryLockerAdapter) Acquire(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error) {
	if a == nil || a.l == nil {
		return nil, false, errors.New("queue: locker is nil")
	}

	lockCtx := ctx
	var timeoutCancel context.CancelFunc
	if ttl > 0 {
		lockCtx, timeoutCancel = context.WithTimeout(ctx, ttl)
	}

	_, cancel, err := a.l.TryLock(lockCtx, key)
	if err != nil {
		if timeoutCancel != nil {
			timeoutCancel()
		}
		if errors.Is(err, cache.ErrNotLocked) {
			return nil, false, nil
		}
		return nil, false, err
	}

	release := func(_ context.Context) error {
		cancel()
		if timeoutCancel != nil {
			timeoutCancel()
		}
		return nil
	}

	return release, true, nil
}

// TryLock 尝试获取分布式锁并在持有锁期间执行 fn。
// 若锁已被占用，则跳过执行并返回 acquired=false, err=nil。
func TryLock(ctx context.Context, locker Locker, key string, ttl time.Duration, fn func(ctx context.Context) error) (bool, error) {
	if locker == nil {
		return true, fn(ctx)
	}

	release, acquired, err := locker.Acquire(ctx, key, ttl)
	if err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}
	defer func() {
		_ = release(ctx)
	}()

	return true, fn(ctx)
}
