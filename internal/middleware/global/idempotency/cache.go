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

// NewCacheLocker 用平台缓存实现跨实例互斥锁。
func NewCacheLocker(store cache.Store, ttl time.Duration) Locker {
	if ttl <= 0 {
		ttl = defaultLockTTL
	}
	return &cacheLocker{
		store:  store,
		ttl:    ttl,
		tokens: make(map[string]string),
	}
}

// cacheLocker 基于 SetNX 抢占、DeleteIfMatch 释放的分布式锁。
type cacheLocker struct {
	store  cache.Store
	ttl    time.Duration
	mu     sync.Mutex
	tokens map[string]string
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
			l.tokens[key] = token
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
	token, ok := l.tokens[key]
	delete(l.tokens, key)
	l.mu.Unlock()
	if !ok {
		return nil
	}

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
