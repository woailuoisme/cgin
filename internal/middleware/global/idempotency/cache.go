package idempotency

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"cgin/pkg/cache"
)

const (
	// cacheResponseNamespace 响应缓存键的业务命名空间。
	cacheResponseNamespace = "idempotency"
	// defaultLockTTL 分布式锁的默认租期。
	defaultLockTTL = 30 * time.Second
	// lockRetryInterval 分布式锁的轮询间隔。
	lockRetryInterval = 10 * time.Millisecond
	// unlockTimeout 请求取消后仍允许完成解锁的时间预算。
	unlockTimeout = 2 * time.Second
)

// NewCacheStorage 用平台缓存实现幂等响应存储，供多实例共享回放结果。
func NewCacheStorage(store cache.Store) Storage {
	return &cacheStorage{store: store}
}

// cacheStorage 将幂等响应存入平台缓存。
type cacheStorage struct {
	store cache.Store
}

// Get 读取响应缓存；未命中时返回 (nil, nil)。
func (s *cacheStorage) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := s.store.Get(ctx, cacheResponseKey(key))
	if errors.Is(err, cache.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(value), nil
}

// Set 写入响应缓存。
func (s *cacheStorage) Set(ctx context.Context, key string, val []byte, exp time.Duration) error {
	if exp <= 0 {
		return fmt.Errorf("idempotency: cache ttl must be positive, got %s", exp)
	}
	return s.store.Set(ctx, cacheResponseKey(key), string(val), exp)
}

// NewCacheLocker 用平台缓存实现跨实例互斥锁；若底层缓存自身暴露 Locker（如 MemoryLocker），则直接委托避免轮询。
func NewCacheLocker(store cache.Store, ttl time.Duration) Locker {
	if ttl <= 0 {
		ttl = defaultLockTTL
	}
	if provider, ok := store.(cache.LockProvider); ok && provider.Locker() != nil {
		return &nativeLockerAdapter{inner: provider.Locker(), ttl: ttl}
	}
	return &cacheLocker{store: store, ttl: ttl}
}

// holderStack 记录每个键的持有者栈，支持同一键重入；零值可直接使用。
type holderStack[T any] struct {
	mu     sync.Mutex
	stacks map[string][]T
}

// push 压入持有者，供嵌套获取同一把锁时按层释放。
func (s *holderStack[T]) push(key string, value T) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stacks == nil {
		s.stacks = make(map[string][]T)
	}
	s.stacks[key] = append(s.stacks[key], value)
}

// pop 弹出并返回栈顶持有者；栈空时返回 false。
func (s *holderStack[T]) pop(key string) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var zero T
	stack := s.stacks[key]
	if len(stack) == 0 {
		return zero, false
	}

	top := stack[len(stack)-1]
	if len(stack) == 1 {
		delete(s.stacks, key)
	} else {
		s.stacks[key] = stack[:len(stack)-1]
	}
	return top, true
}

// nativeLockerAdapter 将平台 cache.Locker 适配为幂等中间件 Locker，消除 10ms 轮询。
type nativeLockerAdapter struct {
	inner   cache.Locker
	ttl     time.Duration
	holders holderStack[context.CancelFunc]
}

// Lock 阻塞等待底层锁并在租期内持有。
func (l *nativeLockerAdapter) Lock(ctx context.Context, key string) error {
	lockCtx, cancelTimeout := l.lockContext(ctx)
	_, release, err := l.inner.Lock(lockCtx, cacheLockKey(key))
	if err != nil {
		cancelTimeout()
		return err
	}

	l.holders.push(key, func() {
		release()
		cancelTimeout()
	})
	return nil
}

// lockContext 给等待加上租期超时；ttl 未配置时直接复用入参上下文。
func (l *nativeLockerAdapter) lockContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if l.ttl <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, l.ttl)
}

// Unlock 释放当前持有的锁。
func (l *nativeLockerAdapter) Unlock(_ context.Context, key string) error {
	release, ok := l.holders.pop(key)
	if !ok {
		return nil
	}

	release()
	return nil
}

// cacheLocker 基于 SetNX 抢占、DeleteIfMatch 释放的分布式锁。
type cacheLocker struct {
	store   cache.Store
	ttl     time.Duration
	holders holderStack[string]
}

// Lock 轮询抢占锁，直到成功或请求上下文取消。
func (l *cacheLocker) Lock(ctx context.Context, key string) error {
	token, err := newLockToken()
	if err != nil {
		return err
	}

	lockKey := cacheLockKey(key)
	for {
		acquired, err := l.store.SetNX(ctx, lockKey, token, l.ttl)
		if err != nil {
			return err
		}
		if acquired {
			l.holders.push(key, token)
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lockRetryInterval):
		}
	}
}

// Unlock 仅删除自己持有的锁，避免误删过期后他人重新获取的锁。
func (l *cacheLocker) Unlock(ctx context.Context, key string) error {
	token, ok := l.holders.pop(key)
	if !ok {
		return nil
	}

	// 请求即使已取消也要完成解锁，否则锁会一直阻塞到租期结束。
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
	defer cancel()

	_, err := l.store.DeleteIfMatch(unlockCtx, cacheLockKey(key), token)
	return err
}

// cacheResponseKey 拼接响应缓存键。
func cacheResponseKey(key string) string {
	return cache.Key(cacheResponseNamespace, "response", key)
}

// cacheLockKey 拼接分布式锁键。
func cacheLockKey(key string) string {
	return cache.Key(cacheResponseNamespace, "lock", key)
}

// newLockToken 生成锁持有者令牌。
func newLockToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("idempotency: generate lock token failed: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

var (
	_ Storage = (*cacheStorage)(nil)
	_ Locker  = (*cacheLocker)(nil)
)
