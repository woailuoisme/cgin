package wechat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bgin/internal/platform/errs"
)

// Session 是小程序登录成功后取得的用户身份。
//
// SessionKey 是对称会话密钥，仅供服务端解密与数据验签使用，绝对严禁下发给前端。
// 本结构在字段级别声明 json:"-"，并实现了 fmt.Stringer 与 fmt.GoStringer 进行强制脱敏，
// 杜绝因序列化、日志打印或反射操作导致的意外密钥泄露。
type Session struct {
	// OpenID 用户在该小程序内的唯一标识。
	OpenID string `json:"openid"`
	// UnionID 用户在微信开放平台账号下的唯一标识。
	UnionID string `json:"unionid,omitempty"`
	// SessionKey 会话密钥，仅供服务端使用。
	SessionKey string `json:"-"`
}

// String 格式化 Session 并脱敏输出 SessionKey。
func (s *Session) String() string {
	if s == nil {
		return "<nil>"
	}
	return fmt.Sprintf("Session{OpenID:%q, UnionID:%q, SessionKey:\"[REDACTED]\"}", s.OpenID, s.UnionID)
}

// GoString 实现 fmt.GoStringer 保证调试输出安全。
func (s *Session) GoString() string {
	return s.String()
}

// authService 实现 AuthService 接口。
type authService struct {
	client *Client
}

var _ AuthService = (*authService)(nil)

// Code2Session 用 wx.login() 返回的 code 校验并换取用户身份。
//
// 微信规定 code 只能使用一次且 5 分钟有效，因此本方法不做重试。
func (s *authService) Code2Session(ctx context.Context, code string) (*Session, error) {
	const operation = "wechat.auth.Code2Session"

	if s == nil || s.client == nil || s.client.http == nil {
		return nil, errs.ServiceUnavailable("wechat miniprogram client is not configured")
	}

	trimmedCode := strings.TrimSpace(code)
	if trimmedCode == "" {
		return nil, errs.BadRequest("code is required")
	}

	response, err := s.client.http.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"appid":      s.client.appID,
			"secret":     s.client.appSecret,
			"js_code":    trimmedCode,
			"grant_type": grantTypeAuthorizationCode,
		}).
		Get(code2SessionPath)
	if err != nil {
		return nil, errs.ServiceUnavailable("wechat code2session request failed: " + s.client.redactError(err))
	}

	var payload code2SessionResponse
	if _, decodeErr := decodeBusinessResponse(operation, response, &payload); decodeErr != nil {
		return nil, decodeErr
	}

	if payload.OpenID == "" {
		return nil, errs.Internal(errors.New("wechat code2session succeeded without openid"))
	}

	return &Session{
		OpenID:     payload.OpenID,
		UnionID:    payload.UnionID,
		SessionKey: payload.SessionKey,
	}, nil
}

// code2SessionResponse 是 code2session 的响应报文。
type code2SessionResponse struct {
	upstreamError
	OpenID     string `json:"openid"`
	SessionKey string `json:"session_key"`
	UnionID    string `json:"unionid"`
}
