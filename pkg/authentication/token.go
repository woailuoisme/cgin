// Package authentication 提供无状态/有状态双模 JWT 令牌编解码、吊销黑名单与多通道认证守卫。
package authentication

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/samber/oops"

	"cgin/internal/config"
	"cgin/pkg/cache"
	"cgin/pkg/errs"
)

// TokenType 区分访问令牌与刷新令牌，防止刷新令牌被直接当作访问令牌使用。
type TokenType string

const (
	// TypeAccess 标识短期访问令牌。
	TypeAccess TokenType = "access"
	// TypeRefresh 标识长期刷新令牌。
	TypeRefresh TokenType = "refresh"

	defaultAccessTTL     = 15 * time.Minute
	defaultRefreshTTL    = 7 * 24 * time.Hour
	defaultUserRevokeTTL = 7 * 24 * time.Hour

	blacklistKeyPrefix  = "auth:blacklist:jti:"
	userRevokeKeyPrefix = "auth:blacklist:user_epoch:"
)

// Claims 是编码进 JWT 的标准载荷与扩展声明。
type Claims struct {
	jwt.RegisteredClaims
	Role              string         `json:"role,omitempty"`
	Type              TokenType      `json:"type"`
	IssuedAtUnixMilli int64          `json:"iat_ms,omitempty"`
	CustomClaims      map[string]any `json:"custom,omitempty"`
}

// IssuedAtTime 返回令牌签发时间，优先采用毫秒级精度 IssuedAtUnixMilli，缺省时回退至 RegisteredClaims.IssuedAt。
func (c *Claims) IssuedAtTime() time.Time {
	if c == nil {
		return time.Time{}
	}
	if c.IssuedAtUnixMilli > 0 {
		return time.UnixMilli(c.IssuedAtUnixMilli)
	}
	if c.IssuedAt != nil {
		return c.IssuedAt.Time
	}
	return time.Time{}
}

// GetClaim 从自定义声明中提取指定键的值。
func (c *Claims) GetClaim(key string) (any, bool) {
	if c == nil || c.CustomClaims == nil {
		return nil, false
	}
	v, ok := c.CustomClaims[key]
	return v, ok
}

// Payload 将标准声明与自定义声明合并为扁平字典。
func (c *Claims) Payload() map[string]any {
	if c == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(c.CustomClaims)+7)
	maps.Copy(out, c.CustomClaims)
	if c.ID != "" {
		out["jti"] = c.ID
	}
	if c.Subject != "" {
		out["sub"] = c.Subject
	}
	if c.Issuer != "" {
		out["iss"] = c.Issuer
	}
	if c.Role != "" {
		out["role"] = c.Role
	}
	if c.Type != "" {
		out["type"] = string(c.Type)
	}
	if c.IssuedAt != nil {
		out["iat"] = c.IssuedAt.Unix()
	}
	if c.ExpiresAt != nil {
		out["exp"] = c.ExpiresAt.Unix()
	}
	return out
}

// TokenPair 表示一次签发产出的访问令牌与刷新令牌组合。
type TokenPair struct {
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

// BlacklistStore 定义令牌黑名单所需的最小缓存存储契约（直接兼容 pkg/cache.Store）。
type BlacklistStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	Exists(ctx context.Context, key string) (bool, error)
}

// TokenIssuer 统一负责 JWT 双令牌签发、验签解析与黑名单吊销管理。
type TokenIssuer struct {
	secret        []byte
	issuer        string
	accessTTL     time.Duration
	refreshTTL    time.Duration
	blacklist     BlacklistStore
	userRevokeTTL time.Duration
}

// NewTokenIssuer 构造 JWT 令牌签发与黑名单管理器（blacklist 允许为 nil，退化为纯无状态模式）。
func NewTokenIssuer(cfg config.JWTConfig, blacklist BlacklistStore) *TokenIssuer {
	accessTTL := cmp.Or(cfg.AccessTTL, defaultAccessTTL)
	refreshTTL := cmp.Or(cfg.RefreshTTL, defaultRefreshTTL)
	return &TokenIssuer{
		secret:        []byte(cfg.Secret),
		issuer:        cfg.Issuer,
		accessTTL:     accessTTL,
		refreshTTL:    refreshTTL,
		blacklist:     blacklist,
		userRevokeTTL: max(refreshTTL, defaultUserRevokeTTL),
	}
}

// AccessTTL 返回默认访问令牌有效期。
func (t *TokenIssuer) AccessTTL() time.Duration { return t.accessTTL }

// RefreshTTL 返回默认刷新令牌有效期。
func (t *TokenIssuer) RefreshTTL() time.Duration { return t.refreshTTL }

// Issue 为指定用户与角色签发默认寿命的访问令牌与刷新令牌。
func (t *TokenIssuer) Issue(userID, role string) (TokenPair, error) {
	return t.IssueWithCustom(userID, role, t.accessTTL, t.refreshTTL, nil, true)
}

// IssueWithCustom 按自定义寿命、扩展声明及刷新令牌开关签发令牌对。
func (t *TokenIssuer) IssueWithCustom(
	userID, role string,
	accessTTL, refreshTTL time.Duration,
	customClaims map[string]any,
	issueRefresh bool,
) (TokenPair, error) {
	accessTTL = cmp.Or(accessTTL, t.accessTTL)
	refreshTTL = cmp.Or(refreshTTL, t.refreshTTL)

	now := time.Now().UTC()
	accessExp := now.Add(accessTTL)

	access, err := t.sign(userID, role, TypeAccess, now, accessExp, customClaims)
	if err != nil {
		return TokenPair{}, err
	}

	if !issueRefresh {
		return TokenPair{
			AccessToken:     access,
			AccessExpiresAt: accessExp,
		}, nil
	}

	refreshExp := now.Add(refreshTTL)
	refresh, err := t.sign(userID, role, TypeRefresh, now, refreshExp, customClaims)
	if err != nil {
		return TokenPair{}, err
	}

	return TokenPair{
		AccessToken:      access,
		RefreshToken:     refresh,
		AccessExpiresAt:  accessExp,
		RefreshExpiresAt: refreshExp,
	}, nil
}

// Parse 校验签名、签发方、算法与令牌类别，返回解析后的 Claims。
func (t *TokenIssuer) Parse(raw string, expected TokenType) (*Claims, error) {
	if raw == "" {
		return nil, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is missing")
	}

	claims := &Claims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(_ *jwt.Token) (any, error) {
		return t.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(t.issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, errs.Unauthorized(errs.CodeAuthTokenExpired, "The token has expired")
		}
		return nil, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
	}
	if !token.Valid || claims.Type != expected || claims.Subject == "" || claims.ID == "" {
		return nil, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
	}
	return claims, nil
}

// AuthenticateToken 解析访问令牌、检查吊销状态并将认证身份注入上下文。
func (t *TokenIssuer) AuthenticateToken(ctx context.Context, rawToken string) (context.Context, *Claims, error) {
	claims, err := t.Parse(rawToken, TypeAccess)
	if err != nil {
		return ctx, nil, err
	}

	revoked, err := t.IsTokenRevoked(ctx, claims)
	if err != nil {
		return ctx, nil, oops.In("authentication").Wrapf(err, "check token revocation")
	}
	if revoked {
		return ctx, nil, errs.Unauthorized(errs.CodeAuthTokenRevoked, "The session has been revoked")
	}

	outCtx := WithToken(ctx, rawToken)
	outCtx = WithIdentity(outCtx, Identity{
		UserID:       claims.Subject,
		Role:         claims.Role,
		CustomClaims: claims.CustomClaims,
		Claims:       claims,
	})
	return outCtx, claims, nil
}

// Revoke 依据 Claims 的 JTI 与剩余寿命将令牌加入黑名单。
func (t *TokenIssuer) Revoke(ctx context.Context, claims *Claims) error {
	if t == nil || t.blacklist == nil || claims == nil || claims.ID == "" {
		return nil
	}
	if claims.ExpiresAt == nil {
		return t.RevokeByID(ctx, claims.ID, defaultAccessTTL)
	}
	remaining := time.Until(claims.ExpiresAt.Time)
	if remaining <= 0 {
		return nil
	}
	return t.RevokeByID(ctx, claims.ID, remaining)
}

// RevokeByID 按指定 JTI 与 TTL 将令牌拉黑。
func (t *TokenIssuer) RevokeByID(ctx context.Context, jti string, ttl time.Duration) error {
	if t == nil || t.blacklist == nil || jti == "" || ttl <= 0 {
		return nil
	}
	if err := t.blacklist.Set(ctx, blacklistKeyPrefix+jti, "revoked", ttl); err != nil {
		return oops.In("authentication").With("jti", jti).Wrapf(err, "revoke token jti")
	}
	return nil
}

// IsRevoked 检查指定 JTI 是否在黑名单中。
func (t *TokenIssuer) IsRevoked(ctx context.Context, jti string) (bool, error) {
	if t == nil || t.blacklist == nil || jti == "" {
		return false, nil
	}
	exists, err := t.blacklist.Exists(ctx, blacklistKeyPrefix+jti)
	if err != nil {
		return false, oops.In("authentication").With("jti", jti).Wrapf(err, "check token blacklist")
	}
	return exists, nil
}

// RevokeUser 刷新指定用户的注销纪元时间戳，使此前签发的所有令牌批量失效。
func (t *TokenIssuer) RevokeUser(ctx context.Context, userID string) error {
	if t == nil || t.blacklist == nil || userID == "" {
		return nil
	}
	epoch := strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
	if err := t.blacklist.Set(ctx, userRevokeKeyPrefix+userID, epoch, t.userRevokeTTL); err != nil {
		return oops.In("authentication").With("user_id", userID).Wrapf(err, "revoke user epoch")
	}
	return nil
}

// IsUserRevoked 检查指定用户在 issuedAt 之前是否触发过全局注销纪元。
func (t *TokenIssuer) IsUserRevoked(ctx context.Context, userID string, issuedAt time.Time) (bool, error) {
	if t == nil || t.blacklist == nil || userID == "" || issuedAt.IsZero() {
		return false, nil
	}
	val, err := t.blacklist.Get(ctx, userRevokeKeyPrefix+userID)
	if err != nil {
		if errors.Is(err, cache.ErrNotFound) {
			return false, nil
		}
		return false, oops.In("authentication").With("user_id", userID).Wrapf(err, "get user revocation epoch")
	}
	if val == "" {
		return false, nil
	}
	revokedAtMilli, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return false, oops.In("authentication").With("user_id", userID).Wrapf(err, "parse user revocation epoch")
	}
	return issuedAt.UnixMilli() <= revokedAtMilli, nil
}

// IsTokenRevoked 综合检查单枚令牌 JTI 黑名单与所属用户的全局注销纪元。
func (t *TokenIssuer) IsTokenRevoked(ctx context.Context, claims *Claims) (bool, error) {
	if t == nil || t.blacklist == nil || claims == nil {
		return false, nil
	}
	revoked, err := t.IsRevoked(ctx, claims.ID)
	if err != nil || revoked {
		return revoked, err
	}
	if claims.Subject != "" {
		return t.IsUserRevoked(ctx, claims.Subject, claims.IssuedAtTime())
	}
	return false, nil
}

func (t *TokenIssuer) sign(
	userID, role string,
	typ TokenType,
	issuedAt, expiresAt time.Time,
	customClaims map[string]any,
) (string, error) {
	jti, err := uuid.NewV7()
	if err != nil {
		return "", errs.Internal(fmt.Errorf("generate jti: %w", err))
	}

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti.String(),
			Issuer:    t.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
		Role:              role,
		Type:              typ,
		IssuedAtUnixMilli: issuedAt.UnixMilli(),
		CustomClaims:      customClaims,
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(t.secret)
	if err != nil {
		return "", errs.Internal(fmt.Errorf("sign jwt: %w", err))
	}
	return signed, nil
}
