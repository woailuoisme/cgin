package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"bgin/internal/platform/cache"
)

// Locker 定义分布式互斥锁契约。
type Locker interface {
	// Acquire 尝试以非阻塞方式获取指定 key 的分布式锁。
	// 获取成功时返回释放函数和 acquired=true；已被占用时返回 acquired=false, err=nil。
	Acquire(ctx context.Context, key string, ttl time.Duration) (release func(context.Context) error, acquired bool, err error)
}

// TryLocker 抽象支持非阻塞 TryLock 契约的互斥锁管理器（符合 ISP 接口隔离原则，可直接对接 cache.Locker）。
type TryLocker interface {
	TryLock(ctx context.Context, name string) (context.Context, context.CancelFunc, error)
}

// AdaptLocker 将实现了 TryLocker 契约的互斥锁（如 cache.Locker）适配为 queue.Locker。
// 借助底层 Watchdog 看门狗自动续期能力，可彻底避免长时间任务执行期间锁意外过期导致的并发重叠。
func AdaptLocker(l TryLocker) Locker {
	if l == nil {
		return nil
	}
	return &tryLockerAdapter{l: l}
}

type tryLockerAdapter struct {
	l TryLocker
}

var _ Locker = (*tryLockerAdapter)(nil)

func (a *tryLockerAdapter) Acquire(ctx context.Context, key string, _ time.Duration) (func(context.Context) error, bool, error) {
	if a == nil || a.l == nil {
		return nil, false, errors.New("queue: locker is nil")
	}

	_, cancel, err := a.l.TryLock(ctx, key)
	if err != nil {
		if errors.Is(err, cache.ErrNotLocked) {
			return nil, false, nil
		}
		return nil, false, err
	}

	release := func(_ context.Context) error {
		cancel()
		return nil
	}

	return release, true, nil
}

// TryLock 尝试获取分布式锁并在持有锁期间执行 fn。
// 若锁已被占用，则跳过执行并返回 acquired=false, err=nil。
func TryLock(ctx context.Context, locker Locker, key string, ttl time.Duration, fn func(ctx context.Context) error) (bool, error) {
	if locker == nil {
		return true, fn(ctx)
	}

	release, acquired, err := locker.Acquire(ctx, key, ttl)
	if err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}
	defer func() {
		_ = release(ctx)
	}()

	return true, fn(ctx)
}

// RedisLocker 基于 Redis SET NX PX 与 Lua 脚本提供安全释放机制的分布式锁实现。
type RedisLocker struct {
	client redis.UniversalClient
}

var _ Locker = (*RedisLocker)(nil)

// NewRedisLocker 创建基于 Redis 的分布式锁实例。
func NewRedisLocker(client redis.UniversalClient) *RedisLocker {
	return &RedisLocker{client: client}
}

// luaReleaseLock 确保只有持有锁的客户端（token 匹配）才能删除释放锁。
const luaReleaseLock = `
if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("del", KEYS[1])
else
    return 0
end
`

// Acquire 尝试通过 Redis SET key token NX PX ttl 获取锁。
func (l *RedisLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error) {
	if l == nil || l.client == nil {
		return nil, false, errors.New("queue: redis client is nil")
	}

	token := randomToken()
	ok, err := l.client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}

	release := func(relCtx context.Context) error {
		return l.client.Eval(relCtx, luaReleaseLock, []string{key}, token).Err()
	}

	return release, true, nil
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(b)
}
