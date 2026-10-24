// Package wechat 提供微信开放平台（公众平台 / 小程序）服务端 API 客户端实现。
package wechat

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-resty/resty/v2"
	"golang.org/x/sync/singleflight"

	"bgin/internal/config"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/errs"
)

const (
	// defaultBaseURL 是微信开放平台服务端 API 的默认域名。
	defaultBaseURL = "https://api.weixin.qq.com"
	// code2SessionPath 是小程序登录凭证校验端点，用 wx.login() 的 code 换取用户身份。
	code2SessionPath = "/sns/jscode2session"
	// grantTypeAuthorizationCode 是 code2Session 的唯一合法取值。
	grantTypeAuthorizationCode = "authorization_code"
	// defaultTimeout 是单次调用微信开放平台 API 的超时。
	defaultTimeout = 5 * time.Second
)

// 微信开放平台业务错误码常量。这些错误码随 HTTP 200 返回，不体现在状态码上。
const (
	errCodeSystemBusy        = -1
	errCodeInvalidCredential = 40001 // 凭据无效或已不是最新的
	errCodeInvalidAppID      = 40013
	errCodeInvalidToken      = 40014 // 无效的 access_token
	errCodeInvalidCode       = 40029
	errCodeInvalidSecret     = 40125
	errCodeHighRiskUser      = 40226
	errCodeInvalidPage       = 41030 // 页面路径不存在、未发布或带了前导斜杠
	errCodeTokenExpired      = 42001 // access_token 已超时
	errCodeAPIFreqLimit      = 45009 // 接口调用频率或日配额超限
	errCodeTooManyRequests   = 45011 // 接口分钟级配额超限
	errCodeAPIUnauthorized   = 48001 // 小程序未获得该接口权限
)

// credentialFailureCodes 是「刷新凭据后值得重试一次」的错误码集合。
var credentialFailureCodes = []int{errCodeInvalidCredential, errCodeInvalidToken, errCodeTokenExpired}

// Client 是微信小程序 API 客户端，实现 MiniProgramClient 门面契约。
//
// 进程级单例持有：持有 AccessToken 的两级缓存、singleflight 回源控制与连接池。
type Client struct {
	appID      string
	appSecret  string
	http       *resty.Client
	tokenStore TokenStore
	l1Token    atomic.Pointer[tokenEntry]
	tokenKey   string
	flight     singleflight.Group

	// 细粒度子服务实例
	auth    *authService
	phone   *phoneService
	wxaCode *wxaCodeService
}

var _ MiniProgramClient = (*Client)(nil)

// NewClient 依据小程序凭据构造客户端。
//
// 凭据未声明时返回 nil，调用方据此判定小程序能力未启用。
func NewClient(cfg config.WeChatMiniProgramConfig, cacheClient cache.Store, opts ...Option) *Client {
	if !cfg.Declared() {
		return nil
	}

	appID := strings.TrimSpace(cfg.AppID)
	appSecret := strings.TrimSpace(cfg.AppSecret)

	httpClient := resty.New().
		SetBaseURL(defaultBaseURL).
		SetTimeout(defaultTimeout).
		SetRetryCount(2).
		SetRetryWaitTime(100 * time.Millisecond).
		SetRetryMaxWaitTime(2 * time.Second)

	c := &Client{
		appID:      appID,
		appSecret:  appSecret,
		http:       httpClient,
		tokenStore: newRedisTokenStore(cacheClient),
		tokenKey:   tokenCacheKey(appID),
	}

	c.auth = &authService{client: c}
	c.phone = &phoneService{client: c}
	c.wxaCode = &wxaCodeService{client: c}

	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}

	return c
}

// AppID 返回该客户端绑定的小程序 AppID。
func (c *Client) AppID() string {
	if c == nil {
		return ""
	}

	return c.appID
}

// Auth 获取认证子模块。
func (c *Client) Auth() AuthService {
	if c == nil {
		return nil
	}

	return c.auth
}

// Phone 获取手机号子模块。
func (c *Client) Phone() PhoneService {
	if c == nil {
		return nil
	}

	return c.phone
}

// WxaCode 获取小程序码子模块。
func (c *Client) WxaCode() WxaCodeService {
	if c == nil {
		return nil
	}

	return c.wxaCode
}

// WithBaseURL 覆盖默认域名，返回同一实例以便链式调用与单元测试起桩。
func (c *Client) WithBaseURL(baseURL string) *Client {
	WithBaseURL(baseURL)(c)
	return c
}

// Code2Session 用 wx.login() 的 code 校验并换取用户身份（兼容便捷方法）。
func (c *Client) Code2Session(ctx context.Context, code string) (*Session, error) {
	if c == nil || c.auth == nil {
		return nil, errs.ServiceUnavailable("wechat miniprogram client is not configured")
	}

	return c.auth.Code2Session(ctx, code)
}

// GetPhoneNumber 用小程序端 getPhoneNumber 回调的 code 换取用户手机号（兼容便捷方法）。
func (c *Client) GetPhoneNumber(ctx context.Context, code string) (*PhoneInfo, error) {
	if c == nil || c.phone == nil {
		return nil, errs.ServiceUnavailable("wechat miniprogram client is not configured")
	}

	return c.phone.GetPhoneNumber(ctx, code)
}

// GetUnlimitedCode 生成数量不限的小程序码（兼容便捷方法）。
func (c *Client) GetUnlimitedCode(ctx context.Context, params UnlimitedCodeParams) ([]byte, error) {
	if c == nil || c.wxaCode == nil {
		return nil, errs.ServiceUnavailable("wechat miniprogram client is not configured")
	}

	return c.wxaCode.GetUnlimited(ctx, params)
}

// withAccessToken 执行一次带 access_token 的业务调用。
func (c *Client) withAccessToken(ctx context.Context, call func(ctx context.Context, token string) (int, error)) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}

	code, callErr := call(ctx, token)
	if code == 0 || !slices.Contains(credentialFailureCodes, code) {
		return callErr
	}

	// 凭据失效重试恰好一次
	c.invalidateAccessToken(ctx)

	retryToken, refreshErr := c.accessToken(ctx)
	if refreshErr != nil {
		return refreshErr
	}

	_, retryErr := call(ctx, retryToken)
	return retryErr
}

// redactError 从 error 中抹除 AppSecret，避免包含密钥的网络错误进入日志。
func (c *Client) redactError(err error) string {
	if err == nil {
		return ""
	}
	if c == nil || c.appSecret == "" {
		return err.Error()
	}

	return strings.ReplaceAll(err.Error(), c.appSecret, "[redacted]")
}

// upstreamError 是所有微信响应报文共有的业务错误字段。
type upstreamError struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func (e *upstreamError) upstreamErrcode() (int, string) {
	return e.ErrCode, e.ErrMsg
}

type errCarrier interface {
	upstreamErrcode() (int, string)
}

// decodeBusinessResponse 解出上游响应体并判定业务成败。
func decodeBusinessResponse(operation string, response *resty.Response, payload errCarrier) (int, error) {
	if err := json.Unmarshal(response.Body(), payload); err != nil {
		return 0, errs.Internal(fmt.Errorf("failed to decode wechat response (status %d): %w", response.StatusCode(), err))
	}

	code, message := payload.upstreamErrcode()
	if code != 0 {
		return code, mapUpstreamError(operation, code, message)
	}

	return 0, nil
}

// mapUpstreamError 把微信业务错误码映射为系统统一错误语义。
func mapUpstreamError(_ string, code int, message string) error {
	switch code {
	case errCodeInvalidCode:
		return errs.BadRequest("wechat: code is invalid, expired or already used")
	case errCodeInvalidPage:
		return errs.BadRequest("wechat: page path does not exist or is not published")
	case errCodeHighRiskUser:
		return errs.Forbidden("wechat: user is flagged as high risk")
	case errCodeAPIFreqLimit, errCodeTooManyRequests:
		return errs.RateLimited("wechat: api rate limit exceeded")
	case errCodeSystemBusy:
		return errs.ServiceUnavailable("wechat: system busy, retry later")
	case errCodeInvalidCredential, errCodeInvalidToken, errCodeTokenExpired:
		return errs.Internal(fmt.Errorf("wechat: credential rejected after refresh: %s (%d)", message, code))
	case errCodeInvalidAppID, errCodeInvalidSecret:
		return errs.Internal(fmt.Errorf("wechat: credential rejected by upstream: %s (%d)", message, code))
	case errCodeAPIUnauthorized:
		return errs.Internal(fmt.Errorf("wechat: miniprogram is not authorized for this api: %s (%d)", message, code))
	default:
		return errs.ServiceUnavailable("wechat: unexpected upstream error")
	}
}
