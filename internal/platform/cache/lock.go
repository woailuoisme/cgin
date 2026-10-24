// Package cache 提供高性能缓存存储与分布式互斥锁支持。
package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/rueidis/rueidislock"

	"bgin/internal/config"
)

var (
	// ErrNotLocked 表示在非阻塞模式下尝试获取锁失败（已被其它协程/节点抢占）。
	ErrNotLocked = errors.New("cache: not locked")
	// ErrLockerClosed 表示锁管理器已关闭，无法继续加锁。
	ErrLockerClosed = errors.New("cache: locker closed")
)

// Locker 统一互斥锁契约接口（支持分布式与纯内存实现，符合 ISP 与 DIP 原则）。
type Locker interface {
	// Lock 阻塞等待并获取指定名称的互斥锁，成功时返回派生的锁生命周期上下文与释放函数。
	Lock(ctx context.Context, name string) (context.Context, context.CancelFunc, error)
	// TryLock 尝试非阻塞获取指定名称的互斥锁，若已被占用则立即返回 ErrNotLocked。
	TryLock(ctx context.Context, name string) (context.Context, context.CancelFunc, error)
	// Close 释放锁管理器关联的资源或后台连接。
	Close() error
}

// DoWithLock 在阻塞获取互斥锁成功后执行业务函数，并在退出时自动安全释放锁。
func DoWithLock(ctx context.Context, locker Locker, name string, fn func(ctx context.Context) error) error {
	lockedCtx, cancel, err := locker.Lock(ctx, name)
	if err != nil {
		return err
	}
	defer cancel()
	return fn(lockedCtx)
}

// DoTryLock 在非阻塞抢占互斥锁成功后执行业务函数，并在退出时自动安全释放锁；若未抢到锁返回 ErrNotLocked。
func DoTryLock(ctx context.Context, locker Locker, name string, fn func(ctx context.Context) error) error {
	lockedCtx, cancel, err := locker.TryLock(ctx, name)
	if err != nil {
		return err
	}
	defer cancel()
	return fn(lockedCtx)
}

// RedisLocker 基于 rueidislock 实现的生产级分布式互斥锁。
// 借助 Redis 客户端缓存（Client-Side Caching）失效通知消除网络轮询开销，
// 并内置 Watchdog 自动续期与原子 Lua 解锁保障。
type RedisLocker struct {
	inner rueidislock.Locker
}

// NewRedisLocker 创建基于 rueidis 的分布式互斥锁管理器。
func NewRedisLocker(cfg config.CacheConfig, opts ...func(*rueidislock.LockerOption)) (*RedisLocker, error) {
	clientOpt, err := clientOption(cfg.URL)
	if err != nil {
		return nil, err
	}

	lockOpt := rueidislock.LockerOption{
		ClientOption: clientOpt,
		KeyPrefix:    namespace + ":lock:",
		KeyValidity:  5 * time.Second,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(&lockOpt)
		}
	}

	inner, err := rueidislock.NewLocker(lockOpt)
	if err != nil {
		return nil, fmt.Errorf("cache: create redis locker failed: %w", err)
	}

	return &RedisLocker{inner: inner}, nil
}

// Lock 阻塞等待并获取分布式锁。
func (r *RedisLocker) Lock(ctx context.Context, name string) (context.Context, context.CancelFunc, error) {
	lockedCtx, cancel, err := r.inner.WithContext(ctx, name)
	if err != nil {
		return nil, nil, mapLockerError(err)
	}
	return lockedCtx, cancel, nil
}

// TryLock 尝试非阻塞获取分布式锁。
func (r *RedisLocker) TryLock(ctx context.Context, name string) (context.Context, context.CancelFunc, error) {
	lockedCtx, cancel, err := r.inner.TryWithContext(ctx, name)
	if err != nil {
		return nil, nil, mapLockerError(err)
	}
	return lockedCtx, cancel, nil
}

func mapLockerError(err error) error {
	switch {
	case errors.Is(err, rueidislock.ErrNotLocked):
		return ErrNotLocked
	case errors.Is(err, rueidislock.ErrLockerClosed):
		return ErrLockerClosed
	default:
		return err
	}
}

// Close 关闭底层 rueidis 客户端。
func (r *RedisLocker) Close() error {
	r.inner.Close()
	return nil
}

// MemoryLocker 纯内存互斥锁管理器，适用于单元测试与单机无 Redis 场景。
type MemoryLocker struct {
	mu     sync.Mutex
	locks  map[string]chan struct{}
	closed bool
}

// NewMemoryLocker 创建纯内存互斥锁管理器。
func NewMemoryLocker() *MemoryLocker {
	return &MemoryLocker{
		locks: make(map[string]chan struct{}),
	}
}

func (m *MemoryLocker) getLockChan(name string) (chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrLockerClosed
	}
	ch, ok := m.locks[name]
	if !ok {
		ch = make(chan struct{}, 1)
		ch <- struct{}{}
		m.locks[name] = ch
	}
	return ch, nil
}

func makeLockerRelease(ctx context.Context, ch chan struct{}) (context.Context, context.CancelFunc) {
	lockedCtx, cancel := context.WithCancel(ctx)
	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel()
			ch <- struct{}{}
		})
	}
	return lockedCtx, release
}

// Lock 阻塞等待并获取指定名称的内存锁。
func (m *MemoryLocker) Lock(ctx context.Context, name string) (context.Context, context.CancelFunc, error) {
	ch, err := m.getLockChan(name)
	if err != nil {
		return nil, nil, err
	}

	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-ch:
		lockedCtx, release := makeLockerRelease(ctx, ch)
		return lockedCtx, release, nil
	}
}

// TryLock 尝试非阻塞获取内存锁。
func (m *MemoryLocker) TryLock(ctx context.Context, name string) (context.Context, context.CancelFunc, error) {
	ch, err := m.getLockChan(name)
	if err != nil {
		return nil, nil, err
	}

	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-ch:
		lockedCtx, release := makeLockerRelease(ctx, ch)
		return lockedCtx, release, nil
	default:
		return nil, nil, ErrNotLocked
	}
}

// Close 关闭内存锁管理器。
func (m *MemoryLocker) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// 静态接口实现校验。
var (
	_ Locker = (*RedisLocker)(nil)
	_ Locker = (*MemoryLocker)(nil)
)
