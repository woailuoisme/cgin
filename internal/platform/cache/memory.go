package cache

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/samber/hot"
	"golang.org/x/sync/singleflight"
)

var errClosed = errors.New("cache: memory store is closed")

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
// 内置 WTinyLFU 驱逐策略、容量上限保护、TTL 抖动清理与并发安全保障。
type MemoryStore struct {
	hot    *hot.HotCache[string, memoryItem]
	group  singleflight.Group
	mu     sync.Mutex // 保护 Increment / SetNX 竞态与关闭状态
	closed bool
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
		WithJanitor().
		Build()

	return &MemoryStore{
		hot: h,
	}
}

// 确保 *MemoryStore 完整实现 Store 契约。
var _ Store = (*MemoryStore)(nil)

// Driver 返回内存驱动标识。
func (m *MemoryStore) Driver() string {
	return "memory"
}

// Ping 检查内存驱动可用性（遵循 ctx 取消信号）。
func (m *MemoryStore) Ping(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errClosed
	}
	return ctx.Err()
}

// Close 释放内存与清理协程。
func (m *MemoryStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	m.hot.StopJanitor()
	m.hot.Purge()
	return nil
}

// Get 读取指定键的值，键不存在或已过期返回 ErrNotFound。
func (m *MemoryStore) Get(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", errClosed
	}
	m.mu.Unlock()

	item, found, err := m.hot.Get(key)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNotFound
	}

	now := time.Now()
	if item.isExpired(now) {
		m.hot.Delete(key)
		return "", ErrNotFound
	}

	return item.value, nil
}

// Set 写入键值并设置过期时间。ttl 必须大于 0。
func (m *MemoryStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ttl <= 0 {
		return fmt.Errorf("cache: ttl for key %s must be positive, got %s", key, ttl)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errClosed
	}

	item := memoryItem{
		value:     value,
		expiresAt: time.Now().Add(ttl),
	}
	m.hot.SetWithTTL(key, item, ttl)
	return nil
}

// SetNX 仅在键不存在或已过期时写入。
func (m *MemoryStore) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if ttl <= 0 {
		return false, fmt.Errorf("cache: ttl for key %s must be positive, got %s", key, ttl)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
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
	if err := ctx.Err(); err != nil {
		return false, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false, errClosed
	}
	m.mu.Unlock()

	item, found := m.hot.Peek(key)
	if !found {
		return false, nil
	}

	return !item.isExpired(time.Now()), nil
}

// Increment 原子自增并保持窗口过期时间：首次创建时设置过期时间，后续自增保留原过期时间。
func (m *MemoryStore) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if ttl <= 0 {
		return 0, fmt.Errorf("cache: ttl for key %s must be positive, got %s", key, ttl)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, errClosed
	}

	now := time.Now()
	item, found := m.hot.Peek(key)
	if !found || item.isExpired(now) {
		exp := now.Add(ttl)
		m.hot.SetWithTTL(key, memoryItem{
			value:     "1",
			expiresAt: exp,
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
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0, errClosed
	}
	m.mu.Unlock()

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
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
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
	if err := ctx.Err(); err != nil {
		return false, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
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
	cached, err := m.Get(ctx, key)
	if err == nil {
		return cached, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", err
	}

	ch := m.group.DoChan(key, func() (any, error) {
		fillCtx := context.WithoutCancel(ctx)

		cached, err := m.Get(fillCtx, key)
		if err == nil {
			return cached, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return "", err
		}

		loaded, err := load(fillCtx)
		if err != nil {
			return "", err
		}

		_ = m.Set(fillCtx, key, loaded, ttl)
		return loaded, nil
	})

	select {
	case res := <-ch:
		if res.Err != nil {
			return "", res.Err
		}
		val, ok := res.Val.(string)
		if !ok {
			return "", errors.New("cache: load result is not a string")
		}
		return val, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Locker 创建关联该纯内存存储环境的互斥锁管理器。
func (m *MemoryStore) Locker() Locker {
	return NewMemoryLocker()
}
