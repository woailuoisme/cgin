package cache

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samber/hot"
	"golang.org/x/sync/singleflight"
)

const defaultMemoryCapacity = 10_000

type memoryItem struct {
	value     string
	expiresAt time.Time // IsZero() 表示无过期时间
}

func (it memoryItem) isExpired(now time.Time) bool {
	if it.expiresAt.IsZero() {
		return false
	}
	return now.After(it.expiresAt)
}

// MemoryStore 基于 samber/hot 的高性能纯内存缓存实现，适用于本地开发、测试与脱机环境。
// 内置 WTinyLFU 驱逐策略、容量上限保护、精确惰性过期与并发安全保障（零后台常驻协程）。
type MemoryStore struct {
	hot    *hot.HotCache[string, memoryItem]
	locker *MemoryLocker
	group  singleflight.Group
	mu     sync.Mutex // 保护 Increment / SetNX / DeleteIfMatch 复合读写原子性
	closed atomic.Bool
}

// NewMemoryStore 创建基于 samber/hot 的纯内存缓存存储实例。
func NewMemoryStore() *MemoryStore {
	return NewMemoryStoreWithCapacity(defaultMemoryCapacity)
}

// NewMemoryStoreWithCapacity 创建指定容量与 WTinyLFU 驱逐策略的纯内存缓存实例。
func NewMemoryStoreWithCapacity(capacity int) *MemoryStore {
	if capacity <= 0 {
		capacity = defaultMemoryCapacity
	}

	h := hot.NewHotCache[string, memoryItem](hot.WTinyLFU, capacity).
		WithTTL(time.Hour).
		Build()

	return &MemoryStore{
		hot:    h,
		locker: NewMemoryLocker(),
	}
}

// 确保 *MemoryStore 完整实现 Store 与 LockProvider 契约。
var (
	_ Store        = (*MemoryStore)(nil)
	_ LockProvider = (*MemoryStore)(nil)
)

// Driver 返回内存驱动标识。
func (m *MemoryStore) Driver() string {
	return "memory"
}

// Ping 检查内存驱动可用性（遵循 ctx 取消信号）。
func (m *MemoryStore) Ping(ctx context.Context) error {
	if m.closed.Load() {
		return errClosed
	}
	return ctx.Err()
}

// Close 释放内存与关联锁资源。
func (m *MemoryStore) Close() error {
	if !m.closed.CompareAndSwap(false, true) {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hot.Purge()
	if m.locker != nil {
		_ = m.locker.Close()
	}
	return nil
}

// Shutdown 实现优雅停机钩子契约。
func (m *MemoryStore) Shutdown() error {
	return m.Close()
}

func (m *MemoryStore) checkReady(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.closed.Load() {
		return errClosed
	}
	return nil
}

// Get 读取指定键的值，键不存在或已过期返回 ErrNotFound。
func (m *MemoryStore) Get(ctx context.Context, key string) (string, error) {
	if err := m.checkReady(ctx); err != nil {
		return "", err
	}

	item, found, err := m.hot.Get(key)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNotFound
	}

	if item.isExpired(time.Now()) {
		m.hot.Delete(key)
		return "", ErrNotFound
	}

	return item.value, nil
}

// Set 写入键值并设置过期时间。ttl 必须大于 0。
func (m *MemoryStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := m.checkReady(ctx); err != nil {
		return err
	}
	if ttl <= 0 {
		return invalidTTLErr(key, ttl)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return errClosed
	}

	m.hot.SetWithTTL(key, memoryItem{
		value:     value,
		expiresAt: time.Now().Add(ttl),
	}, ttl)
	return nil
}

// SetNX 仅在键不存在或已过期时写入。
func (m *MemoryStore) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	if err := m.checkReady(ctx); err != nil {
		return false, err
	}
	if ttl <= 0 {
		return false, invalidTTLErr(key, ttl)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return false, errClosed
	}

	now := time.Now()
	item, found := m.hot.Peek(key)
	if found && !item.isExpired(now) {
		return false, nil
	}

	m.hot.SetWithTTL(key, memoryItem{
		value:     value,
		expiresAt: now.Add(ttl),
	}, ttl)
	return true, nil
}

// Exists 检查键是否存在且未过期。
func (m *MemoryStore) Exists(ctx context.Context, key string) (bool, error) {
	if err := m.checkReady(ctx); err != nil {
		return false, err
	}

	item, found := m.hot.Peek(key)
	if !found {
		return false, nil
	}

	return !item.isExpired(time.Now()), nil
}

// Increment 原子自增并保持窗口过期时间：首次创建时设置过期时间，后续自增保留原过期时间。
func (m *MemoryStore) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	if err := m.checkReady(ctx); err != nil {
		return 0, err
	}
	if ttl <= 0 {
		return 0, invalidTTLErr(key, ttl)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return 0, errClosed
	}

	now := time.Now()
	item, found := m.hot.Peek(key)
	if !found || item.isExpired(now) {
		m.hot.SetWithTTL(key, memoryItem{
			value:     "1",
			expiresAt: now.Add(ttl),
		}, ttl)
		return 1, nil
	}

	curr, err := strconv.ParseInt(item.value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cache: value for key %s is not an integer: %w", key, err)
	}

	curr++
	item.value = strconv.FormatInt(curr, 10)
	remaining := item.expiresAt.Sub(now)
	if remaining <= 0 {
		remaining = ttl
		item.expiresAt = now.Add(ttl)
	}
	m.hot.SetWithTTL(key, item, remaining)
	return curr, nil
}

// TTL 获取剩余有效时间：键不存在或过期返回 0，永久有效返回 -1s。
func (m *MemoryStore) TTL(ctx context.Context, key string) (time.Duration, error) {
	if err := m.checkReady(ctx); err != nil {
		return 0, err
	}

	item, found := m.hot.Peek(key)
	if !found {
		return 0, nil
	}

	now := time.Now()
	if item.isExpired(now) {
		m.hot.Delete(key)
		return 0, nil
	}

	if item.expiresAt.IsZero() {
		return -time.Second, nil
	}

	rem := item.expiresAt.Sub(now)
	if rem <= 0 {
		return 0, nil
	}
	return rem, nil
}

// Delete 删除指定键列表，返回实际有效删除的数量。
func (m *MemoryStore) Delete(ctx context.Context, keys ...string) (int64, error) {
	if err := m.checkReady(ctx); err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return 0, errClosed
	}

	var deleted int64
	now := time.Now()
	for _, key := range keys {
		if item, found := m.hot.Peek(key); found {
			m.hot.Delete(key)
			if !item.isExpired(now) {
				deleted++
			}
		}
	}
	return deleted, nil
}

// DeleteIfMatch 仅当键的当前值等于 expected 时原子删除该键，返回是否真正删除。
func (m *MemoryStore) DeleteIfMatch(ctx context.Context, key, expected string) (bool, error) {
	if err := m.checkReady(ctx); err != nil {
		return false, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed.Load() {
		return false, errClosed
	}

	item, found := m.hot.Peek(key)
	if !found {
		return false, nil
	}

	if item.isExpired(time.Now()) {
		m.hot.Delete(key)
		return false, nil
	}

	if item.value != expected {
		return false, nil
	}

	m.hot.Delete(key)
	return true, nil
}

// Remember 实现 cache-aside 模式，通过 singleflight 防止进程内并发击穿。
func (m *MemoryStore) Remember(
	ctx context.Context,
	key string,
	ttl time.Duration,
	load func(context.Context) (string, error),
) (string, error) {
	return rememberFlight(
		ctx,
		&m.group,
		key,
		func(gCtx context.Context) (string, error) {
			return m.Get(gCtx, key)
		},
		func(fillCtx context.Context) (string, error) {
			loaded, err := load(fillCtx)
			if err != nil {
				return "", err
			}
			_ = m.Set(fillCtx, key, loaded, ttl)
			return loaded, nil
		},
	)
}

// Locker 返回关联该纯内存存储环境的单例互斥锁管理器。
func (m *MemoryStore) Locker() Locker {
	return m.locker
}
