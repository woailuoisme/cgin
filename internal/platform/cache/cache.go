// Package cache 基于 rueidis 封装高性能 Redis/Valkey 客户端，提供键空间、原子读写、计数与防击穿回源。
package cache

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/redis/rueidis"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	"bgin/internal/config"
	"bgin/internal/platform/logger"
)

const (
	// defaultPort REDIS_URL 未指定端口时的默认端口。
	defaultPort = "6379"
	// namespace 统一键命名空间前缀。
	namespace = "bgin"
	// opTimeout 单次 Redis 操作默认超时。
	opTimeout = 500 * time.Millisecond
)

// ErrNotFound 表示键不存在。
var ErrNotFound = errors.New("cache: key not found")

// Client 封装 Redis/Valkey 客户端操作。
type Client struct {
	rdb   rueidis.Client
	group singleflight.Group
}

// Key 拼接带应用命名空间的缓存键（例如 Key("jwt", "revoked", jti) -> bgin:jwt:revoked:<jti>）。
func Key(parts ...string) string {
	if len(parts) == 0 {
		return namespace
	}
	return namespace + ":" + strings.Join(parts, ":")
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

	client := &Client{rdb: rdb}
	if err := client.Ping(ctx); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("cache: ping Redis failed: %w", err)
	}
	return client, nil
}

// clientOption 解析并转换 REDIS_URL 为 rueidis 客户端连接参数。
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

	// 省略端口时补默认端口：rueidis 的 InitAddress 需要 host:port 形式。
	host := parsed.Host
	if parsed.Port() == "" {
		host = net.JoinHostPort(parsed.Hostname(), defaultPort)
	}

	option := rueidis.ClientOption{
		InitAddress: []string{host},
		// 用户名为空时 rueidis 只发 AUTH <password>，对应 "redis://:pass@host" 这种写法；
		// ACL 场景下 URL 里会带用户名，届时按 AUTH <user> <password> 发送。
		Username: parsed.User.Username(),
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

// do 统一注入操作超时并执行命令。
func (c *Client) do(ctx context.Context, cmd rueidis.Completed) rueidis.RedisResult {
	ctx, cancel := withOpTimeout(ctx)
	defer cancel()
	return c.rdb.Do(ctx, cmd)
}

// withOpTimeout 为操作设置超时，若调用方 Context 更紧凑则优先遵循。
func withOpTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= opTimeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, opTimeout)
}

// Close 释放连接池。
func (c *Client) Close() error {
	c.rdb.Close()
	return nil
}

// Ping 探测连接是否可达，供启动期与就绪探针使用。
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

// Set 写入一个带过期时间的键。ttl 必须为正数，避免误写永久键。
func (c *Client) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("cache: ttl for key %s must be positive, got %s", key, ttl)
	}
	return c.do(ctx, c.rdb.B().Set().Key(key).Value(value).Ex(ttl).Build()).Error()
}

// SetNX 仅在键不存在时写入，返回是否写入成功（用于分布式互斥抢占）。
func (c *Client) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, fmt.Errorf("cache: ttl for key %s must be positive, got %s", key, ttl)
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
		return 0, fmt.Errorf("cache: ttl for key %s must be positive, got %s", key, ttl)
	}

	// 向上取整到秒，避免不足 1 秒被 EXPIRE 解析为立即删除。
	seconds := int64((ttl + time.Second - 1) / time.Second)

	resp := incrementScript.Exec(ctx, c.rdb, []string{key}, []string{strconv.FormatInt(seconds, 10)})
	return resp.AsInt64()
}

// DeleteIfMatch 仅当键的当前值等于 expected 时原子删除该键，返回是否真正删除。
//
// 用于分布式锁的安全释放：若持有者的 TTL 已到期、锁被他人重新获取，
// 无条件 DEL 会误删他人的锁，造成互斥同时失效。
func (c *Client) DeleteIfMatch(ctx context.Context, key, expected string) (bool, error) {
	deleted, err := unlockScript.Exec(ctx, c.rdb, []string{key}, []string{expected}).AsInt64()
	if err != nil {
		return false, err
	}
	return deleted > 0, nil
}

// TTL 返回键的剩余寿命：键不存在时返回 0，键存在但没有过期时间时返回 -1s。
func (c *Client) TTL(ctx context.Context, key string) (time.Duration, error) {
	seconds, err := c.do(ctx, c.rdb.B().Ttl().Key(key).Build()).AsInt64()
	if err != nil {
		return 0, err
	}

	switch seconds {
	case -2: // 键不存在
		return 0, nil
	case -1: // 键存在但没有过期时间
		return -time.Second, nil
	default:
		return time.Duration(seconds) * time.Second, nil
	}
}

// Delete 删除若干键，返回实际删除数量。
func (c *Client) Delete(ctx context.Context, keys ...string) (int64, error) {
	// 一个键都不给时直接返回：DEL 不带参数会被服务端当成语法错误。
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
	cached, err := c.Get(ctx, key)
	if err == nil {
		return cached, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", err
	}

	// 使用 DoChan 保证各协程可依据自身 Context 独立退出。
	ch := c.group.DoChan(key, func() (any, error) {
		// 回源操作脱离单次请求取消信号，但保留上下文元数据与日志追踪。
		fillCtx := context.WithoutCancel(ctx)

		// 二次检查缓存，防止并发排队期间已有其它协程写入。
		cached, err := c.Get(fillCtx, key)
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

		if err := c.Set(fillCtx, key, loaded, ttl); err != nil {
			logger.FromContext(fillCtx).Warn("failed to write origin data to cache",
				zap.Error(err),
				zap.String("key", key))
		}
		return loaded, nil
	})

	var result any
	select {
	case res := <-ch:
		if res.Err != nil {
			return "", res.Err
		}
		result = res.Val
	case <-ctx.Done():
		return "", ctx.Err()
	}

	value, ok := result.(string)
	if !ok {
		return "", errors.New("cache: load result is not a string")
	}
	return value, nil
}

// incrementScript 保证自增与设置 TTL 原子完成。
var incrementScript = rueidis.NewLuaScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return count
`)

// unlockScript 保证"比对持有者后再删除"原子完成。
var unlockScript = rueidis.NewLuaScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)
