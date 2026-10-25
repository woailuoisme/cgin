package idempotency

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"cgin/internal/platform/cache"
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
		return &nativeLockerAdapter{
			inner:   provider.Locker(),
			ttl:     ttl,
			cancels: make(map[string][]context.CancelFunc),
		}
	}
	return &cacheLocker{
		store:  store,
		ttl:    ttl,
		tokens: make(map[string][]string),
	}
}

// nativeLockerAdapter 将平台 cache.Locker 适配为幂等中间件 Locker，消除 10ms 轮询。
type nativeLockerAdapter struct {
	inner   cache.Locker
	ttl     time.Duration
	mu      sync.Mutex
	cancels map[string][]context.CancelFunc
}

// Lock 阻塞等待底层锁并在超时上限内持有。
func (l *nativeLockerAdapter) Lock(ctx context.Context, key string) error {
	lockCtx := ctx
	var timeoutCancel context.CancelFunc
	if l.ttl > 0 {
		lockCtx, timeoutCancel = context.WithTimeout(ctx, l.ttl)
	}
	_, cancel, err := l.inner.Lock(lockCtx, cacheLockKey(key))
	if err != nil {
		if timeoutCancel != nil {
			timeoutCancel()
		}
		return err
	}
	l.mu.Lock()
	l.cancels[key] = append(l.cancels[key], func() {
		cancel()
		if timeoutCancel != nil {
			timeoutCancel()
		}
	})
	l.mu.Unlock()
	return nil
}

// Unlock 释放当前持有的锁。
func (l *nativeLockerAdapter) Unlock(_ context.Context, key string) error {
	l.mu.Lock()
	stack := l.cancels[key]
	if len(stack) == 0 {
		l.mu.Unlock()
		return nil
	}
	cancel := stack[len(stack)-1]
	if len(stack) == 1 {
		delete(l.cancels, key)
	} else {
		l.cancels[key] = stack[:len(stack)-1]
	}
	l.mu.Unlock()

	cancel()
	return nil
}

// cacheLocker 基于 SetNX 抢占、DeleteIfMatch 释放的分布式锁。
type cacheLocker struct {
	store  cache.Store
	ttl    time.Duration
	mu     sync.Mutex
	tokens map[string][]string
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
			l.mu.Lock()
			l.tokens[key] = append(l.tokens[key], token)
			l.mu.Unlock()
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lockRetryInterval):
		}
	}
}

// Unlock 仅删除自己持有的锁，避免误删已过期后他人重新获取的锁。
func (l *cacheLocker) Unlock(ctx context.Context, key string) error {
	l.mu.Lock()
	stack := l.tokens[key]
	if len(stack) == 0 {
		l.mu.Unlock()
		return nil
	}
	token := stack[len(stack)-1]
	if len(stack) == 1 {
		delete(l.tokens, key)
	} else {
		l.tokens[key] = stack[:len(stack)-1]
	}
	l.mu.Unlock()

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
