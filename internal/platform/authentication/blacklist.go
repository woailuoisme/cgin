package authentication

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"bgin/internal/platform/cache"
)

// BlacklistStore 抽象令牌与用户撤销黑名单的缓存存取能力。
type BlacklistStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Exists(ctx context.Context, key string) (bool, error)
}

// BlacklistManager 负责黑名单的增删查与用户撤销纪元管理。
type BlacklistManager struct {
	store        BlacklistStore
	refreshTTL   time.Duration
	userRevokeL1 sync.Map // 优化高并发：本地 2 秒 L1 微缓存，减轻远程缓存读穿透
}

type userRevokeL1Entry struct {
	epochUnix int64
	expiresAt time.Time
}

// NewBlacklistManager 构造黑名单管理器。
func NewBlacklistManager(store BlacklistStore, refreshTTL time.Duration) *BlacklistManager {
	if refreshTTL <= 0 {
		refreshTTL = 30 * 24 * time.Hour
	}
	return &BlacklistManager{
		store:      store,
		refreshTTL: refreshTTL,
	}
}

// Store 返回底层 BlacklistStore。
func (b *BlacklistManager) Store() BlacklistStore {
	if b == nil {
		return nil
	}
	return b.store
}

// RevokeToken 将单个令牌 JTI 写入黑名单。
func (b *BlacklistManager) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	if b == nil || b.store == nil || jti == "" {
		return nil
	}
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return nil
	}
	if err := b.store.Set(ctx, cache.Key("jwt", "revoked", jti), "1", ttl); err != nil {
		return fmt.Errorf("authentication: write token blacklist failed: %w", err)
	}
	return nil
}

// IsTokenRevoked 判断令牌 JTI 是否已被加入黑名单。
func (b *BlacklistManager) IsTokenRevoked(ctx context.Context, jti string) (bool, error) {
	if b == nil || b.store == nil || jti == "" {
		return false, nil
	}
	revoked, err := b.store.Exists(ctx, cache.Key("jwt", "revoked", jti))
	if err != nil {
		return false, fmt.Errorf("authentication: check token blacklist failed: %w", err)
	}
	return revoked, nil
}

// RevokeUser 将指定用户的注销纪元写入黑名单缓存，以 O(1) 复杂度使该用户在此之前签发的所有令牌失效。
func (b *BlacklistManager) RevokeUser(ctx context.Context, userID string) error {
	if b == nil || b.store == nil || userID == "" {
		return nil
	}
	now := time.Now().UTC()
	val := strconv.FormatInt(now.UnixMilli(), 10)
	if err := b.store.Set(ctx, cache.Key("jwt", "revoked_user", userID), val, b.refreshTTL); err != nil {
		return fmt.Errorf("authentication: write user revocation epoch failed: %w", err)
	}
	b.setL1(userID, now.UnixMilli(), now)
	return nil
}

// IsUserRevoked 根据令牌签发时间判断用户是否在此之后已被全局注销。
func (b *BlacklistManager) IsUserRevoked(ctx context.Context, userID string, issuedAt time.Time) (bool, error) {
	if b == nil || b.store == nil || userID == "" || issuedAt.IsZero() {
		return false, nil
	}

	normalizeEpoch := func(epoch int64) int64 {
		// 兼容历史秒级纪元（< 100 亿则视为秒级时间戳，转为毫秒）
		if epoch > 0 && epoch < 10_000_000_000 {
			return epoch * 1000
		}
		return epoch
	}

	// 1. 先检查本地 L1 微缓存（TTL 2秒），高并发下极大减免远程缓存读取
	now := time.Now()
	if val, ok := b.userRevokeL1.Load(userID); ok {
		entry, isEntry := val.(userRevokeL1Entry)
		if isEntry && now.Before(entry.expiresAt) {
			if entry.epochUnix == 0 {
				return false, nil
			}
			epochMilli := normalizeEpoch(entry.epochUnix)
			return issuedAt.UnixMilli() <= epochMilli, nil
		}
	}

	// 2. 本地缓存未命中或已过期，回源远程缓存存储
	rawEpoch, err := b.store.Get(ctx, cache.Key("jwt", "revoked_user", userID))
	if err != nil {
		if errors.Is(err, cache.ErrNotFound) {
			b.setL1(userID, 0, now) // 负向微缓存
			return false, nil
		}
		return false, fmt.Errorf("authentication: check user revocation epoch failed: %w", err)
	}

	epochVal, err := strconv.ParseInt(rawEpoch, 10, 64)
	if err != nil {
		return false, fmt.Errorf("authentication: parse user revocation epoch failed: %w", err)
	}

	b.setL1(userID, epochVal, now)
	epochMilli := normalizeEpoch(epochVal)
	return issuedAt.UnixMilli() <= epochMilli, nil
}

func (b *BlacklistManager) setL1(userID string, epochUnix int64, now time.Time) {
	b.userRevokeL1.Store(userID, userRevokeL1Entry{
		epochUnix: epochUnix,
		expiresAt: now.Add(2 * time.Second),
	})
}

// IsRevoked 综合检验令牌 JTI 单项黑名单与用户级全局注销纪元。
func (b *BlacklistManager) IsRevoked(ctx context.Context, claims *Claims) (bool, error) {
	if b == nil || b.store == nil || claims == nil {
		return false, nil
	}
	revoked, err := b.IsTokenRevoked(ctx, claims.ID)
	if err != nil || revoked {
		return revoked, err
	}
	if claims.Subject != "" {
		iat := claims.IssuedAtTime()
		if !iat.IsZero() {
			return b.IsUserRevoked(ctx, claims.Subject, iat)
		}
	}
	return false, nil
}
