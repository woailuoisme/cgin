package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"bgin/internal/platform/cache"
	"bgin/internal/platform/errs"
)

const (
	// stableEndpoint 是稳定版接口调用凭据端点。
	stableEndpoint = "/cgi-bin/stable_token"
	// grantTypeClientCredential 是获取接口调用凭据的唯一合法取值。
	grantTypeClientCredential = "client_credential"
	// tokenRefreshMargin 是提前失效量：提前 5 分钟重取，避免临界请求撞上过期凭据。
	tokenRefreshMargin = 5 * time.Minute
	// minTokenTTL 是本地缓存时长的下限，避免上游返回极短有效期时写入立刻失效的条目。
	minTokenTTL = 30 * time.Second
	// distributedLockTTL 是多 Pod 实例争抢回源时的分布式锁租期。
	distributedLockTTL = 5 * time.Second
	// lockPollInterval 是未争抢到分布式锁的实例轮询 L2 缓存的时间间隔。
	lockPollInterval = 50 * time.Millisecond
	// maxLockWaitDuration 是等待其他 Pod 回源的最大超时时间。
	maxLockWaitDuration = 2 * time.Second
)

// tokenEntry 是两级缓存存储的凭据条目。
type tokenEntry struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (e tokenEntry) isValid(now time.Time) bool {
	return e.Token != "" && now.Before(e.ExpiresAt)
}

// tokenResponse 是 stable_token 的响应报文。
type tokenResponse struct {
	upstreamError
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// tokenCacheKey 生成按 appid 隔离的凭据缓存键。
func tokenCacheKey(appID string) string {
	return cache.Key("wechat", "token", appID)
}

// tokenLockKey 生成按 appid 隔离的分布式排队锁键。
func tokenLockKey(appID string) string {
	return cache.Key("wechat", "token", appID, "lock")
}

// redisTokenStore 基于 internal/platform/cache 实现 TokenStore 接口。
type redisTokenStore struct {
	cache cache.Store
}

var _ TokenStore = (*redisTokenStore)(nil)

func newRedisTokenStore(client cache.Store) TokenStore {
	if client == nil {
		return newMemoryTokenStore()
	}
	return &redisTokenStore{cache: client}
}

func (s *redisTokenStore) Get(ctx context.Context, key string) (string, bool) {
	val, err := s.cache.Get(ctx, key)
	if err != nil || val == "" {
		return "", false
	}
	return val, true
}

func (s *redisTokenStore) Set(ctx context.Context, key string, token string, ttl time.Duration) error {
	return s.cache.Set(ctx, key, token, ttl)
}

func (s *redisTokenStore) Delete(ctx context.Context, key string) error {
	_, err := s.cache.Delete(ctx, key)
	return err
}

func (s *redisTokenStore) Lock(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	owner := newLockOwner()
	acquired, err := s.cache.SetNX(ctx, tokenLockKey(key), owner, ttl)
	if err != nil || !acquired {
		return "", false, err
	}
	return owner, true, nil
}

func (s *redisTokenStore) Unlock(ctx context.Context, key, owner string) error {
	if owner == "" {
		return nil
	}
	// 原子比对持有者后删除：TTL 到期后锁可能已被他人重新获取。
	_, err := s.cache.DeleteIfMatch(ctx, tokenLockKey(key), owner)
	return err
}

// newLockOwner 生成一次锁持有的唯一标识。
func newLockOwner() string {
	return uuid.NewString()
}

type memoryEntry struct {
	value     string
	expiresAt time.Time
}

// memoryTokenStore 基于 sync.Map 实现进程内内存 TokenStore。
type memoryTokenStore struct {
	data      sync.Map
	lockMu    sync.Mutex
	lockOwner atomic.Value
}

var _ TokenStore = (*memoryTokenStore)(nil)

func newMemoryTokenStore() TokenStore {
	return &memoryTokenStore{}
}

func (s *memoryTokenStore) Get(_ context.Context, key string) (string, bool) {
	val, ok := s.data.Load(key)
	if !ok {
		return "", false
	}
	entry, ok := val.(memoryEntry)
	if !ok || time.Now().UTC().After(entry.expiresAt) {
		return "", false
	}
	return entry.value, true
}

func (s *memoryTokenStore) Set(_ context.Context, key string, token string, ttl time.Duration) error {
	s.data.Store(key, memoryEntry{
		value:     token,
		expiresAt: time.Now().UTC().Add(ttl),
	})
	return nil
}

func (s *memoryTokenStore) Delete(_ context.Context, key string) error {
	s.data.Delete(key)
	return nil
}

func (s *memoryTokenStore) Lock(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	if !s.lockMu.TryLock() {
		return "", false, nil
	}
	owner := newLockOwner()
	s.lockOwner.Store(owner)
	return owner, true, nil
}

func (s *memoryTokenStore) Unlock(_ context.Context, _, owner string) error {
	if current, ok := s.lockOwner.Load().(string); ok && current != "" && current != owner {
		return nil
	}
	s.lockOwner.Store("")
	s.lockMu.Unlock()
	return nil
}

// accessToken 返回业务调用所需的接口调用凭据。
//
// 性能架构（L1 进程内存 + L2 分布式 Redis + SingleFlight + Redis 锁）：
// 1. 优先读取基于 atomic.Pointer 的 L1 进程内存缓存（微秒级免 I/O 命中）；
// 2. L1 未命中时读取 L2 分布式存储，命中后回填 L1；
// 3. L2 未命中时，通过进程级 SingleFlight 保证同 Pod 单次回源；
// 4. 回源前尝试争抢分布式排队锁，获锁者回源微信并将结果写入 L1/L2，未获锁者短暂轮询 L2。
func (c *Client) accessToken(ctx context.Context) (string, error) {
	if c == nil {
		return "", errs.ServiceUnavailable("wechat miniprogram client is not configured")
	}

	now := time.Now().UTC()

	// 1. L1 进程内存高速缓存命中
	if l1 := c.l1Token.Load(); l1 != nil && l1.isValid(now) {
		return l1.Token, nil
	}

	// 2. L2 分布式缓存命中
	if token, ok := c.readL2Token(ctx, now); ok {
		return token, nil
	}

	// 3. SingleFlight 进程内合并并发回源
	value, err, _ := c.flight.Do(c.tokenKey, func() (any, error) {
		flightCtx := context.WithoutCancel(ctx)

		// 二次检查 L1 / L2（可能已被同进程前序协程回填）
		if l1 := c.l1Token.Load(); l1 != nil && l1.isValid(time.Now().UTC()) {
			return l1.Token, nil
		}
		if token, ok := c.readL2Token(flightCtx, time.Now().UTC()); ok {
			return token, nil
		}

		// 4. 跨 Pod 分布式锁排队协调
		if c.tokenStore != nil {
			owner, acquired, lockErr := c.tokenStore.Lock(flightCtx, c.tokenKey, distributedLockTTL)
			if lockErr == nil && acquired {
				defer func() {
					_ = c.tokenStore.Unlock(flightCtx, c.tokenKey, owner)
				}()

				return c.fetchAccessToken(flightCtx)
			}

			// 未争抢到分布式锁：等待获锁 Pod 回填，毫秒级轮询 L2
			if token, ok := c.pollL2Token(flightCtx); ok {
				return token, nil
			}
		}

		// 降级：超时未见 L2 回填或未配置分布式锁，本节点直接回源
		return c.fetchAccessToken(flightCtx)
	})
	if err != nil {
		return "", err
	}

	token, _ := value.(string)
	if token == "" {
		return "", errs.Internal(errors.New("wechat returned empty access_token"))
	}

	return token, nil
}

// readL2Token 从 L2 存储读取 Token 并回填 L1。
func (c *Client) readL2Token(ctx context.Context, now time.Time) (string, bool) {
	if c.tokenStore == nil {
		return "", false
	}

	raw, ok := c.tokenStore.Get(ctx, c.tokenKey)
	if !ok || raw == "" {
		return "", false
	}

	var entry tokenEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return "", false
	}

	if !entry.isValid(now) {
		return "", false
	}

	// 回填 L1 进程内存
	c.l1Token.Store(&entry)
	return entry.Token, true
}

// pollL2Token 在未获锁时短暂轮询 L2 缓存。
func (c *Client) pollL2Token(ctx context.Context) (string, bool) {
	ticker := time.NewTicker(lockPollInterval)
	defer ticker.Stop()

	timer := time.NewTimer(maxLockWaitDuration)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", false
		case <-timer.C:
			return "", false
		case <-ticker.C:
			if token, ok := c.readL2Token(ctx, time.Now().UTC()); ok {
				return token, true
			}
		}
	}
}

// fetchAccessToken 向微信 stable_token 接口换取新凭据并写入 L1 与 L2。
func (c *Client) fetchAccessToken(ctx context.Context) (string, error) {
	const operation = "wechat.token.fetchAccessToken"

	response, err := c.http.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"grant_type":    grantTypeClientCredential,
			"appid":         c.appID,
			"secret":        c.appSecret,
			"force_refresh": false,
		}).
		Post(stableEndpoint)
	if err != nil {
		return "", errs.ServiceUnavailable("wechat stable_token request failed: " + c.redactError(err))
	}

	var payload tokenResponse
	if _, decodeErr := decodeBusinessResponse(operation, response, &payload); decodeErr != nil {
		return "", decodeErr
	}

	if payload.AccessToken == "" {
		return "", errs.Internal(errors.New("wechat stable_token succeeded without access_token"))
	}

	ttl := tokenTTL(payload.ExpiresIn)
	entry := tokenEntry{
		Token:     payload.AccessToken,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}

	// 写入 L1 进程内存
	c.l1Token.Store(&entry)

	// 写入 L2 分布式存储
	if c.tokenStore != nil {
		if raw, marshalErr := json.Marshal(entry); marshalErr == nil {
			_ = c.tokenStore.Set(ctx, c.tokenKey, string(raw), ttl)
		}
	}

	return payload.AccessToken, nil
}

// invalidateAccessToken 丢弃 L1 与 L2 凭据缓存。
func (c *Client) invalidateAccessToken(ctx context.Context) {
	if c == nil {
		return
	}

	c.l1Token.Store(nil)
	if c.tokenStore != nil {
		_ = c.tokenStore.Delete(ctx, c.tokenKey)
	}
}

// tokenTTL 计算安全缓存时长。
func tokenTTL(expiresIn int) time.Duration {
	return max(minTokenTTL, time.Duration(expiresIn)*time.Second-tokenRefreshMargin)
}
