// Package authentication 整合身份凭证、令牌生命周期、用户仓储与多守卫认证门禁。
package authentication

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
)

// 令牌类型枚举。两类令牌互不通用，避免刷新令牌被当作访问令牌使用。
const (
	TypeAccess  = "access"
	TypeRefresh = "refresh"
)

// Claims 是令牌载荷结构体。
type Claims struct {
	jwt.RegisteredClaims

	Role              string         `json:"role"`
	Type              string         `json:"tokenType"`
	IssuedAtUnixMilli int64          `json:"iat_ms,omitempty"`
	Custom            map[string]any `json:"custom,omitempty"`
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

// GetClaim 获取指定键名的自定义声明。
func (c *Claims) GetClaim(key string) (any, bool) {
	if c == nil || c.Custom == nil {
		return nil, false
	}
	val, ok := c.Custom[key]
	return val, ok
}

// Payload 返回扁平化的所有声明键值字典。
func (c *Claims) Payload() map[string]any {
	if c == nil {
		return nil
	}
	res := make(map[string]any)
	if c.ID != "" {
		res["jti"] = c.ID
	}
	if c.Issuer != "" {
		res["iss"] = c.Issuer
	}
	if c.Subject != "" {
		res["sub"] = c.Subject
	}
	if c.Role != "" {
		res["role"] = c.Role
	}
	if c.Type != "" {
		res["tokenType"] = c.Type
	}
	if c.ExpiresAt != nil {
		res["exp"] = c.ExpiresAt.Unix()
	}
	if c.IssuedAt != nil {
		res["iat"] = c.IssuedAt.Unix()
	}
	maps.Copy(res, c.Custom)
	return res
}

// TokenPair 是一次签发的访问令牌与刷新令牌凭据。
type TokenPair struct {
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

// JWTCodec 负责纯无状态的 JWT 令牌加密签名与校验解析（纯密码学操作，无外部 I/O 状态）。
type JWTCodec struct {
	secret     []byte
	issuer     string
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewJWTCodec 构造纯无状态 JWT 编解码器。
// 对传入的 secret 执行防御性拷贝，避免外部后续修改底层密钥字节。
func NewJWTCodec(secret []byte, issuer string, accessTTL, refreshTTL time.Duration) *JWTCodec {
	return &JWTCodec{
		secret:     bytes.Clone(secret),
		issuer:     issuer,
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

// IssuePair 签发访问令牌与刷新令牌对。
func (c *JWTCodec) IssuePair(userID, role string, accessTTL, refreshTTL time.Duration, customClaims map[string]any) (TokenPair, error) {
	now := time.Now().UTC()
	if accessTTL <= 0 {
		accessTTL = c.accessTTL
	}
	if refreshTTL <= 0 {
		refreshTTL = c.refreshTTL
	}
	accessExpiresAt := now.Add(accessTTL)
	refreshExpiresAt := now.Add(refreshTTL)

	accessToken, err := c.Sign(userID, role, TypeAccess, now, accessExpiresAt, customClaims)
	if err != nil {
		return TokenPair{}, err
	}
	refreshToken, err := c.Sign(userID, role, TypeRefresh, now, refreshExpiresAt, nil)
	if err != nil {
		return TokenPair{}, err
	}

	return TokenPair{
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		AccessExpiresAt:  accessExpiresAt,
		RefreshExpiresAt: refreshExpiresAt,
	}, nil
}

// Sign 构造带 JTI 与规范声明的 HMAC-SHA256 JWT 令牌。
func (c *JWTCodec) Sign(userID, role, tokenType string, now, expiresAt time.Time, customClaims map[string]any) (string, error) {
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.NewString(),
			Issuer:    c.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
		Role:              role,
		Type:              tokenType,
		IssuedAtUnixMilli: now.UnixMilli(),
		Custom:            customClaims,
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(c.secret)
	if err != nil {
		return "", fmt.Errorf("authentication: sign %s token failed: %w", tokenType, err)
	}
	return signed, nil
}

// Parse 校验并解析原始令牌，校验失败返回标准化 401 错误。
func (c *JWTCodec) Parse(raw, wantType string) (*Claims, error) {
	claims := &Claims{}

	parsed, err := jwt.ParseWithClaims(
		raw, claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
			}
			return c.secret, nil
		},
		jwt.WithIssuer(c.issuer),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, errs.Unauthorized(errs.CodeAuthTokenExpired, "The token has expired")
		}
		return nil, errs.Wrap(errs.CodeAuthTokenInvalid, "The token is invalid", err)
	}
	if !parsed.Valid || claims.Type != wantType {
		return nil, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
	}
	return claims, nil
}

// TokenIssuer 统一编排无状态编解码器 (JWTCodec) 与有状态会话黑名单 (BlacklistManager)。
type TokenIssuer struct {
	codec     *JWTCodec
	blacklist *BlacklistManager
}

// NewTokenIssuer 构造 TokenIssuer。
func NewTokenIssuer(cfg config.JWTConfig, store BlacklistStore) *TokenIssuer {
	codec := NewJWTCodec([]byte(cfg.Secret), cfg.Issuer, cfg.AccessTTL, cfg.RefreshTTL)
	var bl *BlacklistManager
	if store != nil {
		bl = NewBlacklistManager(store, cfg.RefreshTTL)
	}
	return &TokenIssuer{
		codec:     codec,
		blacklist: bl,
	}
}

// Codec 返回底层无状态 JWT 编解码器。
func (i *TokenIssuer) Codec() *JWTCodec {
	return i.codec
}

// Blacklist 返回底层有状态黑名单管理器。
func (i *TokenIssuer) Blacklist() *BlacklistManager {
	return i.blacklist
}

// Issue 签发标准访问令牌与刷新令牌。
func (i *TokenIssuer) Issue(userID, role string) (TokenPair, error) {
	return i.codec.IssuePair(userID, role, 0, 0, nil)
}

// IssueWithCustom 允许覆盖 TTL 与附加自定义声明签发双令牌。
func (i *TokenIssuer) IssueWithCustom(userID, role string, accessTTL, refreshTTL time.Duration, customClaims map[string]any) (TokenPair, error) {
	return i.codec.IssuePair(userID, role, accessTTL, refreshTTL, customClaims)
}

// Parse 校验并解析原始令牌，校验失败返回标准化 401 错误。
func (i *TokenIssuer) Parse(raw, wantType string) (*Claims, error) {
	return i.codec.Parse(raw, wantType)
}

// Revoke 将令牌加入黑名单。
func (i *TokenIssuer) Revoke(ctx context.Context, claims *Claims) error {
	if claims == nil {
		return nil
	}
	return i.RevokeByID(ctx, claims.ID, i.expiryOf(claims))
}

// RevokeByID 根据令牌 JTI 与到期时间将其写入撤销黑名单。
func (i *TokenIssuer) RevokeByID(ctx context.Context, jti string, expiresAt time.Time) error {
	if i.blacklist == nil {
		return nil
	}
	return i.blacklist.RevokeToken(ctx, jti, expiresAt)
}

// IsRevoked 判断令牌是否在撤销黑名单中。
func (i *TokenIssuer) IsRevoked(ctx context.Context, jti string) (bool, error) {
	if i.blacklist == nil {
		return false, nil
	}
	return i.blacklist.IsTokenRevoked(ctx, jti)
}

// RevokeUser 将指定用户的注销纪元写入黑名单缓存，以 O(1) 复杂度使该用户在此之前签发的所有令牌失效。
func (i *TokenIssuer) RevokeUser(ctx context.Context, userID string) error {
	if i.blacklist == nil {
		return nil
	}
	return i.blacklist.RevokeUser(ctx, userID)
}

// IsUserRevoked 根据令牌签发时间判断用户是否在此之后已被全局注销。
func (i *TokenIssuer) IsUserRevoked(ctx context.Context, userID string, issuedAt time.Time) (bool, error) {
	if i.blacklist == nil {
		return false, nil
	}
	return i.blacklist.IsUserRevoked(ctx, userID, issuedAt)
}

// IsTokenRevoked 综合检验令牌 JTI 单项黑名单与用户级全局注销纪元。
func (i *TokenIssuer) IsTokenRevoked(ctx context.Context, claims *Claims) (bool, error) {
	if i.blacklist == nil {
		return false, nil
	}
	return i.blacklist.IsRevoked(ctx, claims)
}

// AuthenticateToken 校验原始访问令牌、检查黑名单并将解析出的 Identity 注入请求上下文。
func (i *TokenIssuer) AuthenticateToken(ctx context.Context, rawToken string) (context.Context, *Claims, error) {
	claims, err := i.Parse(rawToken, TypeAccess)
	if err != nil {
		return ctx, nil, err
	}

	if i.blacklist != nil {
		revoked, err := i.blacklist.IsRevoked(ctx, claims)
		if err != nil {
			return ctx, nil, errs.Internal(err)
		}
		if revoked {
			return ctx, nil, errs.Unauthorized(errs.CodeAuthTokenRevoked, "The token has been revoked")
		}
	}

	var expiresAt time.Time
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time
	}

	newCtx := With(ctx, Identity{
		UserID:         claims.Subject,
		Role:           claims.Role,
		TokenID:        claims.ID,
		TokenExpiresAt: expiresAt,
		Claims:         claims,
	})

	return newCtx, claims, nil
}

// expiryOf 提取令牌过期时间。
func (i *TokenIssuer) expiryOf(claims *Claims) time.Time {
	if claims == nil || claims.ExpiresAt == nil {
		return time.Time{}
	}
	return claims.ExpiresAt.Time
}
