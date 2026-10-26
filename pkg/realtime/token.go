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
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return []byte(secret), nil
		},
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

func numericDateToTime(nd *jwt.NumericDate) time.Time {
	if nd != nil {
		return nd.Time
	}
	return time.Time{}
}
