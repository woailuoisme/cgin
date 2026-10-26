package idempotency

import (
	"context"
	"sync"
)

// Locker 以字符串键串行化并发请求。
// 方法接收 ctx，使分布式实现可以在请求取消时停止等待。
type Locker interface {
	// Lock 获取指定键的独占锁，必要时阻塞等待。
	Lock(ctx context.Context, key string) error
	// Unlock 释放指定键的独占锁。
	Unlock(ctx context.Context, key string) error
}

// countedLock 记录单键互斥量与持有/排队计数。
type countedLock struct {
	mu     sync.Mutex
	locked int
}

// MemoryLock 是进程内幂等键互斥实现，只在单实例内有效。
type MemoryLock struct {
	keys map[string]*countedLock
	mu   sync.Mutex
}

// NewMemoryLock 构造进程内互斥锁。
func NewMemoryLock() *MemoryLock {
	return &MemoryLock{keys: make(map[string]*countedLock)}
}

// Lock 获取指定键的锁；等待者计入引用，避免删除仍被等待的键。
func (l *MemoryLock) Lock(_ context.Context, key string) error {
	lock := l.retain(key)
	lock.mu.Lock()
	return nil
}

// Unlock 释放指定键的锁，最后一个持有者退出时清理映射。
func (l *MemoryLock) Unlock(_ context.Context, key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	lock, ok := l.keys[key]
	if !ok {
		return nil
	}

	lock.mu.Unlock()
	lock.locked--
	if lock.locked <= 0 {
		delete(l.keys, key)
	}
	return nil
}

// retain 获取或初始化指定键的计数锁并递增引用计数。
func (l *MemoryLock) retain(key string) *countedLock {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.keys == nil {
		l.keys = make(map[string]*countedLock)
	}
	lock, ok := l.keys[key]
	if !ok {
		lock = &countedLock{}
		l.keys[key] = lock
	}
	lock.locked++
	return lock
}

var _ Locker = (*MemoryLock)(nil)
