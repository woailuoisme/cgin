// Package cache 提供高性能缓存存储、泛型防击穿/防穿透辅助与分布式互斥锁契约。
package cache

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// namespace 统一键命名空间前缀。
	namespace = "cgin"
	// negativeSentinel 是负缓存（Anti-Penetration）的内部占位哨兵。
	negativeSentinel = "\x00__cgin_cache_nil__\x00"
)

var (
	// ErrNotFound 表示键不存在。
	ErrNotFound = errors.New("cache: key not found")
	// ErrNotLocked 表示在非阻塞模式下尝试获取锁失败（已被其它协程/节点抢占）。
	ErrNotLocked = errors.New("cache: not locked")
	// ErrLockerClosed 表示锁管理器已关闭，无法继续加锁。
	ErrLockerClosed = errors.New("cache: locker closed")
	// errClosed 表示缓存存储已关闭。
	errClosed = errors.New("cache: store is closed")
)

// Store 统一缓存存储契约接口（支持分布式 Valkey/Redis 与纯内存实现）。
type Store interface {
	// Driver 返回当前底层驱动标识（例如 "valkey", "memory"）。
	Driver() string
	// Ping 探测缓存服务连通性。
	Ping(ctx context.Context) error
	// Get 读取键的值；键不存在时返回 ErrNotFound。
	Get(ctx context.Context, key string) (string, error)
	// Set 写入一个带过期时间的键。ttl 必须为正数。
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	// SetNX 仅在键不存在时写入，返回是否写入成功（用于分布式抢占/幂等控制）。
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	// Exists 判断键是否存在。
	Exists(ctx context.Context, key string) (bool, error)
	// Increment 原子自增并绑定/保留过期时间窗口。
	Increment(ctx context.Context, key string, ttl time.Duration) (int64, error)
	// TTL 返回键的剩余寿命：键不存在返回 0，永久有效返回 -1s。
	TTL(ctx context.Context, key string) (time.Duration, error)
	// Delete 删除若干键，返回实际删除数量。
	Delete(ctx context.Context, keys ...string) (int64, error)
	// Remember 防击穿回源读取或计算写入。
	Remember(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (string, error)) (string, error)
	// Close 释放连接或清理资源。
	Close() error
}

// Locker 统一互斥锁契约接口（支持分布式 rueidislock 与纯内存实现）。
type Locker interface {
	// Lock 阻塞等待并获取指定名称的互斥锁，成功时返回派生的锁生命周期上下文与释放函数。
	Lock(ctx context.Context, name string) (context.Context, context.CancelFunc, error)
	// TryLock 尝试非阻塞获取指定名称的互斥锁，若已被占用则立即返回 ErrNotLocked。
	TryLock(ctx context.Context, name string) (context.Context, context.CancelFunc, error)
	// Close 释放锁管理器关联的资源或后台连接。
	Close() error
}

// LockProvider 声明缓存存储可选暴露的互斥锁管理器能力。
type LockProvider interface {
	// Locker 返回与当前存储后端关联的互斥锁管理器。
	Locker() Locker
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

// Key 拼接带应用命名空间的缓存键（例如 Key("jwt", "revoked", jti) -> cfiber:jwt:revoked:<jti>）。
func Key(parts ...string) string {
	if len(parts) == 0 {
		return namespace
	}
	return namespace + ":" + strings.Join(parts, ":")
}

// GetJSON 从 Store 中读取并反序列化 JSON 结构体对象；键不存在或命中负缓存哨兵时返回 ErrNotFound。
func GetJSON[T any](ctx context.Context, s Store, key string) (T, error) {
	var zero T
	str, err := s.Get(ctx, key)
	if err != nil {
		return zero, err
	}
	if str == negativeSentinel {
		return zero, ErrNotFound
	}
	var val T
	if err := json.Unmarshal([]byte(str), &val); err != nil {
		return zero, fmt.Errorf("cache: unmarshal json for key %s failed: %w", key, err)
	}
	return val, nil
}

// SetJSON 将任意 Go 结构体对象序列化为 JSON 写入 Store。
func SetJSON[T any](ctx context.Context, s Store, key string, value T, ttl time.Duration) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cache: marshal json for key %s failed: %w", key, err)
	}
	return s.Set(ctx, key, string(b), ttl)
}

// RememberJSON 实现泛型对象的 cache-aside 模式，结合 singleflight 防止高并发缓存击穿。
func RememberJSON[T any](
	ctx context.Context,
	s Store,
	key string,
	ttl time.Duration,
	load func(context.Context) (T, error),
) (T, error) {
	return RememberJSONAllowMissing(ctx, s, key, ttl, 0, load)
}

// RememberJSONAllowMissing 在 RememberJSON 基础上增加防缓存穿透（Anti-Penetration）负缓存支持。
func RememberJSONAllowMissing[T any](
	ctx context.Context,
	s Store,
	key string,
	ttl time.Duration,
	missingTTL time.Duration,
	load func(context.Context) (T, error),
) (T, error) {
	var zero T
	val, err := GetJSON[T](ctx, s, key)
	if err == nil {
		return val, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return zero, err
	}

	var (
		loadedVal T
		loadedOK  bool
	)

	str, err := s.Remember(ctx, key, ttl, func(fillCtx context.Context) (string, error) {
		loaded, loadErr := load(fillCtx)
		if loadErr != nil {
			if errors.Is(loadErr, ErrNotFound) && missingTTL > 0 {
				_ = s.Set(fillCtx, key, negativeSentinel, missingTTL)
			}
			return "", loadErr
		}
		b, mErr := json.Marshal(loaded)
		if mErr != nil {
			return "", fmt.Errorf("cache: marshal json failed in remember: %w", mErr)
		}
		loadedVal = loaded
		loadedOK = true
		return string(b), nil
	})
	if err != nil {
		return zero, err
	}
	if str == negativeSentinel {
		return zero, ErrNotFound
	}
	if loadedOK {
		return loadedVal, nil
	}

	var result T
	if err := json.Unmarshal([]byte(str), &result); err != nil {
		return zero, fmt.Errorf("cache: unmarshal json failed in remember: %w", err)
	}
	return result, nil
}

// JitterTTL 在基础 TTL 上叠加 [-jitterRatio, +jitterRatio] 范围的随机抖动，防止缓存雪崩。
func JitterTTL(ttl time.Duration, jitterRatio float64) time.Duration {
	if ttl <= 0 || jitterRatio <= 0 {
		return ttl
	}
	if jitterRatio > 0.9 {
		jitterRatio = 0.9
	}
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return ttl
	}
	unit := float64(binary.LittleEndian.Uint64(buf[:])) / float64(^uint64(0))
	delta := (unit*2 - 1) * jitterRatio
	jittered := time.Duration(float64(ttl) * (1 + delta))
	if jittered <= 0 {
		return ttl
	}
	return jittered
}

// rememberFlight 封装带 Context 取消感知与二次缓存检查的 singleflight 防击穿执行内核。
func rememberFlight(
	ctx context.Context,
	group *singleflight.Group,
	flightKey string,
	fastGet func(context.Context) (string, error),
	loadAndStore func(context.Context) (string, error),
) (string, error) {
	if cached, err := fastGet(ctx); err == nil {
		return cached, nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", err
	}

	ch := group.DoChan(flightKey, func() (any, error) {
		fillCtx := context.WithoutCancel(ctx)
		if cached, err := fastGet(fillCtx); err == nil {
			return cached, nil
		} else if !errors.Is(err, ErrNotFound) {
			return "", err
		}
		return loadAndStore(fillCtx)
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

func invalidTTLErr(key string, ttl time.Duration) error {
	return fmt.Errorf("cache: ttl for key %s must be positive, got %s", key, ttl)
}
