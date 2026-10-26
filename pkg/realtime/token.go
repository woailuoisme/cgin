// Package realtime 提供基于 Centrifugo 的高可用实时通信基础设施驱动与协议定义。
package realtime

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ConnectionTokenClaims 定义 Centrifugo 客户端连接标准的 JWT 声明载体。
type ConnectionTokenClaims struct {
	Subject   string         `json:"sub,omitempty"`
	ExpiresAt time.Time      `json:"exp,omitzero"`
	IssuedAt  time.Time      `json:"iat,omitzero"`
	Info      map[string]any `json:"info,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
	Channels  []string       `json:"channels,omitempty"`
}

type rawConnectionClaims struct {
	jwt.RegisteredClaims

	Info     map[string]any `json:"info,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
	Channels []string       `json:"channels,omitempty"`
}

// GenerateConnectionToken 为指定用户签发 Centrifugo HMAC-SHA256 连接凭证。
func GenerateConnectionToken(
	secret string,
	userID string,
	expireAt time.Time,
	info map[string]any,
	meta map[string]any,
	channels ...string,
) (string, error) {
	if secret == "" {
		return "", errors.New("empty secret for connection token generation")
	}

	claims := rawConnectionClaims{
		Info:     info,
		Meta:     meta,
		Channels: channels,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
			ExpiresAt: jwt.NewNumericDate(expireAt),
		},
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString([]byte(secret))
}

// ValidateConnectionToken 验证 Centrifugo 连接凭证并解析出 Claims。
func ValidateConnectionToken(secret, tokenStr string) (*ConnectionTokenClaims, error) {
	if secret == "" {
		return nil, errors.New("empty secret for connection token validation")
	}

	var claims rawConnectionClaims
	tok, err := jwt.ParseWithClaims(
		tokenStr,
		&claims,
		hmacKeyFunc(secret),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	if err != nil {
		return nil, err
	}
	// 校验失败却无错误返回时必须显式失败，不能把 (nil, nil) 交给调用方。
	if tok == nil || !tok.Valid {
		return nil, errors.New("invalid connection token")
	}

	return &ConnectionTokenClaims{
		Subject:   claims.Subject,
		ExpiresAt: numericDateToTime(claims.ExpiresAt),
		IssuedAt:  numericDateToTime(claims.IssuedAt),
		Info:      claims.Info,
		Meta:      claims.Meta,
		Channels:  claims.Channels,
	}, nil
}

// SubscriptionTokenClaims 定义 Centrifugo 客户端订阅特定频道的 JWT 声明。
type SubscriptionTokenClaims struct {
	Subject   string         `json:"sub,omitempty"`
	Channel   string         `json:"channel"`
	Client    string         `json:"client,omitempty"`
	ExpiresAt time.Time      `json:"exp,omitzero"`
	IssuedAt  time.Time      `json:"iat,omitzero"`
	Info      map[string]any `json:"info,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

type rawSubscriptionClaims struct {
	jwt.RegisteredClaims

	Channel string         `json:"channel"`
	Client  string         `json:"client,omitempty"`
	Info    map[string]any `json:"info,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

// SubscriptionTokenOption 定义订阅令牌配置函数。
type SubscriptionTokenOption func(*rawSubscriptionClaims)

// WithSubscriptionInfo 附加客户端公开订阅信息。
func WithSubscriptionInfo(info map[string]any) SubscriptionTokenOption {
	return func(c *rawSubscriptionClaims) {
		c.Info = info
	}
}

// WithSubscriptionMeta 附加服务端私有元数据。
func WithSubscriptionMeta(meta map[string]any) SubscriptionTokenOption {
	return func(c *rawSubscriptionClaims) {
		c.Meta = meta
	}
}

// WithSubscriptionClient 绑定特定客户端连接 ClientID。
func WithSubscriptionClient(clientID string) SubscriptionTokenOption {
	return func(c *rawSubscriptionClaims) {
		c.Client = clientID
	}
}

// GenerateSubscriptionToken 为指定用户签发订阅特定频道的 Centrifugo JWT 凭证。
func GenerateSubscriptionToken(
	secret string,
	userID string,
	channel string,
	expireAt time.Time,
	opts ...SubscriptionTokenOption,
) (string, error) {
	if secret == "" {
		return "", errors.New("empty secret for subscription token generation")
	}
	if channel == "" {
		return "", errors.New("empty channel for subscription token generation")
	}

	claims := rawSubscriptionClaims{
		Channel: channel,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
			ExpiresAt: jwt.NewNumericDate(expireAt),
		},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(&claims)
		}
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString([]byte(secret))
}

// ValidateSubscriptionToken 验证 Centrifugo 订阅凭证并解析出 SubscriptionTokenClaims。
func ValidateSubscriptionToken(secret, tokenStr string) (*SubscriptionTokenClaims, error) {
	if secret == "" {
		return nil, errors.New("empty secret for subscription token validation")
	}

	var claims rawSubscriptionClaims
	tok, err := jwt.ParseWithClaims(
		tokenStr,
		&claims,
		hmacKeyFunc(secret),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	if err != nil {
		return nil, err
	}
	// 校验失败却无错误返回时必须显式失败，不能把 (nil, nil) 交给调用方。
	if tok == nil || !tok.Valid {
		return nil, errors.New("invalid subscription token")
	}

	return &SubscriptionTokenClaims{
		Subject:   claims.Subject,
		Channel:   claims.Channel,
		Client:    claims.Client,
		ExpiresAt: numericDateToTime(claims.ExpiresAt),
		IssuedAt:  numericDateToTime(claims.IssuedAt),
		Info:      claims.Info,
		Meta:      claims.Meta,
	}, nil
}

func numericDateToTime(nd *jwt.NumericDate) time.Time {
	if nd != nil {
		return nd.Time
	}
	return time.Time{}
}

func hmacKeyFunc(secret string) jwt.Keyfunc {
	return func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	}
}
