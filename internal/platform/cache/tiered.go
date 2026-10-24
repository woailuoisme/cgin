// Package cache 提供高性能缓存存储、双级自失效缓存与分布式互斥锁支持。
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/rueidis"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const (
	// defaultTieredL1TTL 默认本地 L1 缓存上限寿命。
	defaultTieredL1TTL = 5 * time.Minute
)

// invalidationMessage 定义跨节点双级缓存主动失效通知消息载荷。
type invalidationMessage struct {
	InstanceID string   `json:"instanceId"`
	Keys       []string `json:"keys"`
}

// TieredOption 为 TieredStore 构造提供函数式配置选项。
type TieredOption func(*TieredStore)

// WithTieredChannel 自定义 Redis Pub/Sub 失效广播频道名称。
func WithTieredChannel(ch string) TieredOption {
	return func(t *TieredStore) {
		if ch != "" {
			t.chName = ch
		}
	}
}

// WithTieredL1TTL 设置本地内存 L1 缓存上限有效期。
func WithTieredL1TTL(ttl time.Duration) TieredOption {
	return func(t *TieredStore) {
		if ttl > 0 {
			t.l1TTL = ttl
		}
	}
}

// WithTieredLogger 注入结构化日志记录器。
func WithTieredLogger(log *zap.Logger) TieredOption {
	return func(t *TieredStore) {
		if log != nil {
			t.log = log
		}
	}
}

// TieredStore 实现多级缓存体系（L1 samber/hot 本地内存 + L2 rueidis 分布式 Redis）。
// 通过 Redis Pub/Sub 广播实现跨节点主动自失效，消除集中式远端缓存网络开销与雪崩击穿风险。
type TieredStore struct {
	l1         *MemoryStore
	l2         *Client
	chName     string
	l1TTL      time.Duration
	instanceID string
	log        *zap.Logger

	group singleflight.Group

	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex
	closed bool
}

// 确保 *TieredStore 完整实现 Store 契约。
var _ Store = (*TieredStore)(nil)

// NewTieredStore 构造基于本地内存 L1 与分布式缓存 L2 的双级自失效缓存实例。
func NewTieredStore(l1 *MemoryStore, l2 *Client, opts ...TieredOption) *TieredStore {
	if l1 == nil {
		l1 = NewMemoryStore()
	}

	t := &TieredStore{
		l1:         l1,
		l2:         l2,
		chName:     Key("cache", "tier", "invalidate"),
		l1TTL:      defaultTieredL1TTL,
		instanceID: uuid.NewString(),
		log:        zap.NewNop(),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}

	if t.l2 != nil && t.l2.rdb != nil {
		ctx, cancel := context.WithCancel(context.Background())
		t.cancel = cancel
		t.wg.Add(1)
		go t.startSubscriber(ctx)
	}

	return t
}

// Driver 返回双级缓存驱动标识。
func (t *TieredStore) Driver() string {
	return "tiered"
}

// L1 返回本地内存存储实例。
func (t *TieredStore) L1() *MemoryStore {
	return t.l1
}

// L2 返回远端分布式存储实例（若未配置可能为 nil）。
func (t *TieredStore) L2() *Client {
	return t.l2
}

// InstanceID 返回当前进程节点的唯一标识符。
func (t *TieredStore) InstanceID() string {
	return t.instanceID
}

// Locker 返回互斥锁管理器（优先适配本地内存环境）。
func (t *TieredStore) Locker() Locker {
	return t.l1.Locker()
}

// Ping 检查双级缓存连通性与可用性。
func (t *TieredStore) Ping(ctx context.Context) error {
	if t.isClosed() {
		return errClosed
	}

	if err := t.l1.Ping(ctx); err != nil {
		return err
	}
	if t.l2 != nil {
		return t.l2.Ping(ctx)
	}
	return nil
}

// Close 安全平滑关闭后台 Pub/Sub 订阅协程并释放存储资源。
func (t *TieredStore) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	if t.cancel != nil {
		t.cancel()
	}
	t.mu.Unlock()

	t.wg.Wait()

	var errs []error
	if err := t.l1.Close(); err != nil {
		errs = append(errs, err)
	}
	if t.l2 != nil {
		if err := t.l2.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Get 读取指定键的值，优先命中本地 L1 内存；Miss 时穿透至分布式 L2 并回填 L1。
func (t *TieredStore) Get(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if t.isClosed() {
		return "", errClosed
	}

	// 1. 优先查本地 L1
	val, err := t.l1.Get(ctx, key)
	if err == nil {
		return val, nil
	}
	if !errors.Is(err, ErrNotFound) && ctx.Err() != nil {
		return "", ctx.Err()
	}

	if t.l2 == nil {
		return "", ErrNotFound
	}

	// 2. L1 Miss，查询远端 L2
	val, err = t.l2.Get(ctx, key)
	if err != nil {
		return "", err
	}

	// 3. 计算回填 L1 TTL（取 L2 剩余 TTL 与 L1 预设上限的较小值）
	l1TTL := t.l1TTL
	if ttl, ttlErr := t.l2.TTL(ctx, key); ttlErr == nil {
		l1TTL = t.effectiveL1TTL(ttl)
	}
	_ = t.l1.Set(ctx, key, val, l1TTL)

	return val, nil
}

// Set 写入键值并设置过期时间：先写 L2，再同步写入 L1，最后广播失效通知。
func (t *TieredStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.isClosed() {
		return errClosed
	}
	if ttl <= 0 {
		return invalidTTLErr(key, ttl)
	}

	if t.l2 != nil {
		if err := t.l2.Set(ctx, key, value, ttl); err != nil {
			return err
		}
	}

	_ = t.l1.Set(ctx, key, value, t.effectiveL1TTL(ttl))

	t.publishInvalidation(ctx, key)
	return nil
}

// SetNX 仅在键不存在时写入：穿透至 L2 保证强一致原子抢占。
func (t *TieredStore) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if t.isClosed() {
		return false, errClosed
	}
	if ttl <= 0 {
		return false, invalidTTLErr(key, ttl)
	}

	if t.l2 != nil {
		ok, err := t.l2.SetNX(ctx, key, value, ttl)
		if err != nil || !ok {
			return ok, err
		}

		_ = t.l1.Set(ctx, key, value, t.effectiveL1TTL(ttl))
		t.publishInvalidation(ctx, key)
		return true, nil
	}

	return t.l1.SetNX(ctx, key, value, ttl)
}

// Exists 检查键是否存在（优先查询 L1，Miss 时查询 L2）。
func (t *TieredStore) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if t.isClosed() {
		return false, errClosed
	}

	if exists, err := t.l1.Exists(ctx, key); err == nil && exists {
		return true, nil
	}

	if t.l2 != nil {
		return t.l2.Exists(ctx, key)
	}
	return false, nil
}

// Increment 执行原子自增，直接穿透至 L2 保证集群强一致，并同步失效 L1。
func (t *TieredStore) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if t.isClosed() {
		return 0, errClosed
	}
	if ttl <= 0 {
		return 0, invalidTTLErr(key, ttl)
	}

	if t.l2 != nil {
		curr, err := t.l2.Increment(ctx, key, ttl)
		if err != nil {
			return 0, err
		}
		_, _ = t.l1.Delete(ctx, key)
		t.publishInvalidation(ctx, key)
		return curr, nil
	}

	return t.l1.Increment(ctx, key, ttl)
}

// TTL 获取键的剩余有效时间，以分布式 L2 事实为准。
func (t *TieredStore) TTL(ctx context.Context, key string) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if t.isClosed() {
		return 0, errClosed
	}

	if t.l2 != nil {
		return t.l2.TTL(ctx, key)
	}
	return t.l1.TTL(ctx, key)
}

// Delete 删除指定键列表，同步清除 L1 与 L2 并广播失效。
func (t *TieredStore) Delete(ctx context.Context, keys ...string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if t.isClosed() {
		return 0, errClosed
	}
	if len(keys) == 0 {
		return 0, nil
	}

	_, _ = t.l1.Delete(ctx, keys...)
	t.publishInvalidation(ctx, keys...)

	if t.l2 != nil {
		return t.l2.Delete(ctx, keys...)
	}
	return 0, nil
}

// DeleteIfMatch 仅当键值匹配时原子删除（用于分布式锁释放）。
func (t *TieredStore) DeleteIfMatch(ctx context.Context, key, expected string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if t.isClosed() {
		return false, errClosed
	}

	if t.l2 != nil {
		deleted, err := t.l2.DeleteIfMatch(ctx, key, expected)
		if err != nil || !deleted {
			return deleted, err
		}
		_, _ = t.l1.Delete(ctx, key)
		t.publishInvalidation(ctx, key)
		return true, nil
	}

	return t.l1.DeleteIfMatch(ctx, key, expected)
}

// Remember 防击穿回源读取或计算写入：本地 singleflight 与 L2 双重防护。
func (t *TieredStore) Remember(
	ctx context.Context,
	key string,
	ttl time.Duration,
	load func(context.Context) (string, error),
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if t.isClosed() {
		return "", errClosed
	}

	// 1. 优先查本地 L1
	if cached, err := t.l1.Get(ctx, key); err == nil {
		return cached, nil
	}

	// 2. 本地 singleflight 聚合，防止并发穿透
	ch := t.group.DoChan(key, func() (any, error) {
		fillCtx := context.WithoutCancel(ctx)

		// 二次检查 L1
		if cached, err := t.l1.Get(fillCtx, key); err == nil {
			return cached, nil
		}

		var loaded string
		var err error

		if t.l2 != nil {
			loaded, err = t.l2.Remember(fillCtx, key, ttl, load)
		} else {
			loaded, err = t.l1.Remember(fillCtx, key, ttl, load)
		}
		if err != nil {
			return "", err
		}

		_ = t.l1.Set(fillCtx, key, loaded, t.effectiveL1TTL(ttl))
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

// publishInvalidation 向集群广播缓存失效消息。
func (t *TieredStore) publishInvalidation(ctx context.Context, keys ...string) {
	if t.l2 == nil || t.l2.rdb == nil || len(keys) == 0 {
		return
	}

	msg := invalidationMessage{
		InstanceID: t.instanceID,
		Keys:       keys,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.log.Warn("tiered cache: marshal invalidation message failed", zap.Error(err))
		return
	}

	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()

	cmd := t.l2.rdb.B().Publish().Channel(t.chName).Message(string(data)).Build()
	if err := t.l2.rdb.Do(pubCtx, cmd).Error(); err != nil {
		t.log.Warn("tiered cache: publish invalidation failed",
			zap.Error(err),
			zap.Strings("keys", keys))
	}
}

// startSubscriber 启动 Redis Pub/Sub 后台监听循环，接收并处理跨节点失效通知。
func (t *TieredStore) startSubscriber(ctx context.Context) {
	defer t.wg.Done()

	for {
		if ctx.Err() != nil {
			return
		}

		err := t.l2.rdb.Receive(ctx, t.l2.rdb.B().Subscribe().Channel(t.chName).Build(), func(msg rueidis.PubSubMessage) {
			t.handleInvalidation(msg.Message)
		})
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return
			}
			t.log.Warn("tiered cache: redis pubsub subscriber disconnected, reconnecting", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

// handleInvalidation 解析失效消息并驱逐本地 L1 缓存。
func (t *TieredStore) handleInvalidation(payload string) {
	if payload == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var msg invalidationMessage
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		// 降级支持：若直接发送裸键名则直接按单键驱逐
		_, _ = t.l1.Delete(ctx, payload)
		return
	}

	// 忽略本节点自己广播的消息（本地已在操作完成时同步处理）
	if msg.InstanceID == t.instanceID {
		return
	}

	if len(msg.Keys) > 0 {
		_, _ = t.l1.Delete(ctx, msg.Keys...)
	}
}

func (t *TieredStore) isClosed() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.closed
}

// effectiveL1TTL 计算本地 L1 缓存的生效寿命，不超过预设上限。
func (t *TieredStore) effectiveL1TTL(ttl time.Duration) time.Duration {
	if ttl > 0 && ttl < t.l1TTL {
		return ttl
	}
	return t.l1TTL
}

func invalidTTLErr(key string, ttl time.Duration) error {
	return fmt.Errorf("cache: ttl for key %s must be positive, got %s", key, ttl)
}
