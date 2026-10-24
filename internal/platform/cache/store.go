package cache

import (
	"context"
	"time"
)

// Store 统一缓存存储契约接口（支持分布式与纯内存实现，符合 ISP 与 DIP 原则）。
type Store interface {
	// Driver 返回当前底层驱动标识（例如 "valkey", "memory"）。
	Driver() string
	// Ping 探测缓存服务连通性。
	Ping(ctx context.Context) error
	// Get 读取键的值；键不存在时返回 ErrNotFound。
	Get(ctx context.Context, key string) (string, error)
	// Set 写入一个带过期时间的键。ttl 必须为正数。
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	// SetNX 仅在键不存在时写入，返回是否写入成功（用于分布式抢占/互斥锁）。
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	// Exists 判断键是否存在。
	Exists(ctx context.Context, key string) (bool, error)
	// Increment 原子自增并绑定/保留过期时间窗口。
	Increment(ctx context.Context, key string, ttl time.Duration) (int64, error)
	// TTL 返回键的剩余寿命：键不存在返回 0，永久有效返回 -1s。
	TTL(ctx context.Context, key string) (time.Duration, error)
	// Delete 删除若干键，返回实际删除数量。
	Delete(ctx context.Context, keys ...string) (int64, error)
	// DeleteIfMatch 仅当键的值与 expected 匹配时原子删除，常用于安全释放分布式互斥锁。
	DeleteIfMatch(ctx context.Context, key, expected string) (bool, error)
	// Remember 防击穿回源读取或计算写入。
	Remember(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (string, error)) (string, error)
	// Close 释放连接或清理资源。
	Close() error
}

// 确保 *Client 完整实现 Store 契约。
var _ Store = (*Client)(nil)
