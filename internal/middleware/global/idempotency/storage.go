package idempotency

import (
	"context"
	"sync"
	"time"
)

// Storage 持久化已完成的响应；未命中时返回 (nil, nil)。
type Storage interface {
	// Get 读取指定键的响应缓存。
	Get(ctx context.Context, key string) ([]byte, error)
	// Set 写入指定键的响应缓存，exp 为保留时长。
	Set(ctx context.Context, key string, val []byte, exp time.Duration) error
}

// memoryEntry 是 MemoryStorage 中的单条带过期时间记录。
type memoryEntry struct {
	value     []byte
	expiresAt time.Time
}

// MemoryStorage 是进程内响应缓存，按 Lifetime 惰性清理过期记录。
type MemoryStorage struct {
	mu       sync.RWMutex
	entries  map[string]memoryEntry
	interval time.Duration
	lastGC   time.Time
}

// NewMemoryStorage 构造进程内响应缓存；interval 用于控制清理频率。
func NewMemoryStorage(lifetime time.Duration) *MemoryStorage {
	return &MemoryStorage{
		entries:  make(map[string]memoryEntry),
		interval: lifetime / 2,
		lastGC:   time.Now(),
	}
}

// Get 读取缓存；不存在或已过期时返回 (nil, nil)。
func (s *MemoryStorage) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	entry, ok := s.entries[key]
	s.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		s.mu.Lock()
		delete(s.entries, key)
		s.mu.Unlock()
		return nil, nil
	}
	return entry.value, nil
}

// Set 写入缓存，并周期性清理过期记录，避免键空间无限增长。
func (s *MemoryStorage) Set(_ context.Context, key string, val []byte, exp time.Duration) error {
	entry := memoryEntry{value: append([]byte(nil), val...)}
	if exp > 0 {
		entry.expiresAt = time.Now().Add(exp)
	}

	s.mu.Lock()
	s.entries[key] = entry
	s.collectLocked()
	s.mu.Unlock()
	return nil
}

// collectLocked 在清理间隔到达时删除全部过期记录；调用方必须持有写锁。
func (s *MemoryStorage) collectLocked() {
	if s.interval <= 0 || time.Since(s.lastGC) < s.interval {
		return
	}
	now := time.Now()
	for key, entry := range s.entries {
		if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
			delete(s.entries, key)
		}
	}
	s.lastGC = now
}

var _ Storage = (*MemoryStorage)(nil)
