package cache

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/rueidis"
	"github.com/redis/rueidis/rueidislock"
	"golang.org/x/sync/singleflight"

	"cgin/internal/config"
	"cgin/pkg/logger"
)

const (
	defaultPort = "6379"
	opTimeout   = 500 * time.Millisecond
)

var (
	_ Store        = (*Client)(nil)
	_ LockProvider = (*Client)(nil)
	_ Locker       = (*RedisLocker)(nil)
)

// incrementScript 保证自增与首次设置 TTL 原子完成。
var incrementScript = rueidis.NewLuaScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return count
`)

// Client 封装 Redis/Valkey 客户端操作。
type Client struct {
	rdb        rueidis.Client
	clientOpt  rueidis.ClientOption
	group      singleflight.Group
	lockerOnce sync.Once
	locker     *RedisLocker
}

// Open 建立连接并执行 PING 探测。
func Open(ctx context.Context, cfg config.CacheConfig) (*Client, error) {
	option, err := clientOption(cfg.URL)
	if err != nil {
		return nil, err
	}

	rdb, err := rueidis.NewClient(option)
	if err != nil {
		return nil, fmt.Errorf("cache: connect Redis failed: %w", err)
	}

	client := &Client{
		rdb:       rdb,
		clientOpt: option,
	}
	if err := client.Ping(ctx); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("cache: ping Redis failed: %w", err)
	}

	return client, nil
}

func clientOption(rawURL string) (rueidis.ClientOption, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rueidis.ClientOption{}, fmt.Errorf("cache: parse REDIS_URL failed: %w", err)
	}

	switch parsed.Scheme {
	case "redis", "rediss":
	default:
		return rueidis.ClientOption{}, fmt.Errorf(
			"cache: unsupported REDIS_URL scheme %q, only redis:// or rediss:// allowed", parsed.Scheme)
	}

	host := parsed.Host
	if parsed.Port() == "" {
		host = net.JoinHostPort(parsed.Hostname(), defaultPort)
	}

	option := rueidis.ClientOption{
		InitAddress: []string{host},
		Username:    parsed.User.Username(),
	}
	if password, ok := parsed.User.Password(); ok {
		option.Password = password
	}

	if db := strings.TrimPrefix(parsed.Path, "/"); db != "" {
		index, err := strconv.Atoi(db)
		if err != nil {
			return rueidis.ClientOption{}, fmt.Errorf("cache: invalid database number %q in REDIS_URL: %w", db, err)
		}
		option.SelectDB = index
	}

	if parsed.Scheme == "rediss" {
		option.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return option, nil
}

func (c *Client) do(ctx context.Context, cmd rueidis.Completed) rueidis.RedisResult {
	ctx, cancel := withOpTimeout(ctx)
	defer cancel()
	return c.rdb.Do(ctx, cmd)
}

func withOpTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= opTimeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, opTimeout)
}

// Close 释放连接池与按需初始化的锁管理器。
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.lockerOnce.Do(func() {})
	if c.locker != nil {
		_ = c.locker.Close()
	}
	if c.rdb != nil {
		c.rdb.Close()
	}
	return nil
}

// Shutdown 释放客户端连接与互斥锁，满足 do.ShutdownerWithError 契约。
func (c *Client) Shutdown() error {
	return c.Close()
}

// Locker 按需惰性初始化并返回与当前存储后端关联的互斥锁管理器。
func (c *Client) Locker() Locker {
	if c == nil || c.rdb == nil {
		return nil
	}
	c.lockerOnce.Do(func() {
		locker, err := newRedisLockerFromOption(c.clientOpt)
		if err == nil {
			c.locker = locker
		}
	})
	if c.locker == nil {
		return nil
	}
	return c.locker
}

// Ping 探测连接是否可达。
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, c.rdb.B().Ping().Build()).Error()
}

// Driver 返回底层驱动标识。
func (c *Client) Driver() string { return "valkey" }

// Get 读取键的值；键不存在时返回 ErrNotFound。
func (c *Client) Get(ctx context.Context, key string) (string, error) {
	value, err := c.do(ctx, c.rdb.B().Get().Key(key).Build()).ToString()
	if err == nil {
		return value, nil
	}
	if errors.Is(err, rueidis.Nil) {
		return "", ErrNotFound
	}
	return "", err
}

// Set 写入一个带过期时间的键。ttl 必须为正数。
func (c *Client) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if ttl <= 0 {
		return invalidTTLErr(key, ttl)
	}
	return c.do(ctx, c.rdb.B().Set().Key(key).Value(value).Ex(ttl).Build()).Error()
}

// SetNX 仅在键不存在时写入，返回是否写入成功。
func (c *Client) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, invalidTTLErr(key, ttl)
	}

	err := c.do(ctx, c.rdb.B().Set().Key(key).Value(value).Nx().Ex(ttl).Build()).Error()
	if err == nil {
		return true, nil
	}
	if errors.Is(err, rueidis.Nil) {
		return false, nil
	}
	return false, err
}

// Exists 判断键是否存在。
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	count, err := c.do(ctx, c.rdb.B().Exists().Key(key).Build()).AsInt64()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// Increment 基于 Lua 脚本原子自增，并在首次创建时绑定过期时间。
func (c *Client) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	if ttl <= 0 {
		return 0, invalidTTLErr(key, ttl)
	}
	seconds := int64((ttl + time.Second - 1) / time.Second)
	resp := incrementScript.Exec(ctx, c.rdb, []string{key}, []string{strconv.FormatInt(seconds, 10)})
	return resp.AsInt64()
}

// TTL 返回键的剩余寿命：键不存在时返回 0，键存在但没有过期时间时返回 -1s。
func (c *Client) TTL(ctx context.Context, key string) (time.Duration, error) {
	seconds, err := c.do(ctx, c.rdb.B().Ttl().Key(key).Build()).AsInt64()
	if err != nil {
		return 0, err
	}
	switch seconds {
	case -2:
		return 0, nil
	case -1:
		return -time.Second, nil
	default:
		return time.Duration(seconds) * time.Second, nil
	}
}

// Delete 删除若干键，返回实际删除数量。
func (c *Client) Delete(ctx context.Context, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	return c.do(ctx, c.rdb.B().Del().Key(keys...).Build()).AsInt64()
}

// Remember 实现 cache-aside 模式，通过 singleflight 防止进程内并发缓存击穿。
func (c *Client) Remember(
	ctx context.Context,
	key string,
	ttl time.Duration,
	load func(context.Context) (string, error),
) (string, error) {
	return rememberFlight(
		ctx,
		&c.group,
		key,
		func(gCtx context.Context) (string, error) {
			return c.Get(gCtx, key)
		},
		func(fillCtx context.Context) (string, error) {
			loaded, err := load(fillCtx)
			if err != nil {
				return "", err
			}
			if err := c.Set(fillCtx, key, loaded, ttl); err != nil {
				logger.FromContext(fillCtx).WarnContext(fillCtx, "failed to write origin data to cache",
					slog.Any("error", err),
					slog.String("key", key),
				)
			}
			return loaded, nil
		},
	)
}

// RedisLocker 基于 rueidislock 实现的生产级分布式互斥锁。
type RedisLocker struct {
	inner rueidislock.Locker
}

// NewRedisLocker 创建基于 rueidis 的分布式互斥锁管理器。
func NewRedisLocker(cfg config.CacheConfig, opts ...func(*rueidislock.LockerOption)) (*RedisLocker, error) {
	clientOpt, err := clientOption(cfg.URL)
	if err != nil {
		return nil, err
	}
	return newRedisLockerFromOption(clientOpt, opts...)
}

func newRedisLockerFromOption(clientOpt rueidis.ClientOption, opts ...func(*rueidislock.LockerOption)) (*RedisLocker, error) {
	lockOpt := rueidislock.LockerOption{
		ClientOption: clientOpt,
		KeyPrefix:    namespace + ":lock:",
		KeyValidity:  5 * time.Second,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&lockOpt)
		}
	}
	inner, err := rueidislock.NewLocker(lockOpt)
	if err != nil {
		return nil, fmt.Errorf("cache: create redis locker failed: %w", err)
	}
	return &RedisLocker{inner: inner}, nil
}

// Lock 阻塞等待并获取分布式锁。
func (r *RedisLocker) Lock(ctx context.Context, name string) (context.Context, context.CancelFunc, error) {
	lockedCtx, cancel, err := r.inner.WithContext(ctx, name)
	if err != nil {
		return nil, nil, mapLockerError(err)
	}
	return lockedCtx, cancel, nil
}

// TryLock 尝试非阻塞获取分布式锁。
func (r *RedisLocker) TryLock(ctx context.Context, name string) (context.Context, context.CancelFunc, error) {
	lockedCtx, cancel, err := r.inner.TryWithContext(ctx, name)
	if err != nil {
		return nil, nil, mapLockerError(err)
	}
	return lockedCtx, cancel, nil
}

// Close 关闭底层 rueidis 客户端。
func (r *RedisLocker) Close() error {
	r.inner.Close()
	return nil
}

func mapLockerError(err error) error {
	switch {
	case errors.Is(err, rueidislock.ErrNotLocked):
		return ErrNotLocked
	case errors.Is(err, rueidislock.ErrLockerClosed):
		return ErrLockerClosed
	default:
		return err
	}
}
