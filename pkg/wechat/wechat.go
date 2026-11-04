// Package wechat 提供微信开放平台（公众平台 / 小程序）服务端 API 客户端与多应用管理基础设施。
//
// 架构指南：
//  1. 全局装配：推荐使用 NewManager(cfg.WeChat, cacheClient) 构造多小程序管理器；
//  2. 领域消费：业务层可通过 mgr.Default() 或 mgr.App("user") 获取 MiniProgramClient；
//  3. 接口隔离：提供细粒度契约接口（AuthService / PhoneService / WxaCodeService），由 *Client 与 *FakeClient 直接实现，无中间胶水包装层；
//  4. 离线开发：单测与本地开发可使用 NewFakeClient() / NewFakeManager()，无需外部网络凭证。
package wechat

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-resty/resty/v2"
	"golang.org/x/sync/singleflight"

	"cgin/internal/config"
	"cgin/pkg/cache"
	"cgin/pkg/errs"
)

const (
	// DefaultAppKey 是默认小程序应用的命名空间标识。
	DefaultAppKey = "default"
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

// AuthService 定义微信小程序身份与登录凭据校验契约 (ISP 细粒度接口)。
//
// 业务切片若仅处理小程序登录，可仅声明依赖此最小接口，无需感知其他能力。
type AuthService interface {
	// Code2Session 用 wx.login() 返回的 code 校验并换取用户身份。
	Code2Session(ctx context.Context, code string) (*Session, error)
}

// PhoneService 定义微信小程序手机号快速验证契约 (ISP 细粒度接口)。
type PhoneService interface {
	// GetPhoneNumber 用小程序端 getPhoneNumber 回调返回的 code 换取用户绑定的手机号。
	GetPhoneNumber(ctx context.Context, code string) (*PhoneInfo, error)
}

// WxaCodeService 定义微信小程序码生成契约 (ISP 细粒度接口)。
type WxaCodeService interface {
	// GetUnlimitedCode 生成数量不限的小程序码，返回 JPEG 字节流。
	GetUnlimitedCode(ctx context.Context, params UnlimitedCodeParams) ([]byte, error)
}

// MiniProgramClient 是微信小程序客户端的聚合契约。
//
// 组合了各细粒度能力接口并直接提供 AppID 标识，由 *Client 与 *FakeClient 直接实现。
type MiniProgramClient interface {
	// AppID 返回该客户端绑定的小程序 AppID。
	AppID() string
	AuthService
	PhoneService
	WxaCodeService
}

// TokenStore 定义 AccessToken 的外部分布式存储与排队锁契约。
type TokenStore interface {
	// Get 读取缓存中的 Token 凭据。
	Get(ctx context.Context, key string) (string, bool)
	// Set 写入 Token 凭据并指定有效期。
	Set(ctx context.Context, key string, token string, ttl time.Duration) error
	// Delete 丢弃指定的 Token 凭据。
	Delete(ctx context.Context, key string) error
	// Lock 尝试获取分布式回源锁，避免多节点并发击穿上游 API；
	// 获取成功时同时返回本次持有的唯一标识，供解锁时校验归属。
	Lock(ctx context.Context, key string, ttl time.Duration) (owner string, acquired bool, err error)
	// Unlock 释放分布式回源锁；仅当锁仍属于该持有者时才会删除，避免误删他人的锁。
	Unlock(ctx context.Context, key, owner string) error
}

// Option 为 Client 提供函数式配置选项。
type Option func(*Client)

// WithBaseURL 设置微信 API 的请求基础域名（主要用于单元测试与桩服务）。
func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		if c != nil && c.http != nil && strings.TrimSpace(baseURL) != "" {
			c.http.SetBaseURL(strings.TrimSuffix(strings.TrimSpace(baseURL), "/"))
		}
	}
}

// WithHTTPClient 覆盖默认的 resty HTTP 客户端。
func WithHTTPClient(client *resty.Client) Option {
	return func(c *Client) {
		if c != nil && client != nil {
			c.http = client
		}
	}
}

// WithTokenStore 注入自定义 TokenStore（如自定义分布式缓存或测试专用 Store）。
func WithTokenStore(store TokenStore) Option {
	return func(c *Client) {
		if c != nil && store != nil {
			c.tokenStore = store
		}
	}
}

// WithTimeout 覆盖默认的请求超时时长。
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		if c != nil && c.http != nil && timeout > 0 {
			c.http.SetTimeout(timeout)
		}
	}
}

// Manager 管理全系统注册的多个微信小程序客户端实例（对齐 payment.Manager 架构）。
//
// 线程安全，支持多小程序多主体注册、按应用名或 AppID 快速检索以及默认应用回退。
type Manager struct {
	defaultApp string
	clients    map[string]MiniProgramClient
	byAppID    map[string]MiniProgramClient
	mu         sync.RWMutex
}

// NewManager 根据 WeChatConfig 构造多应用管理器。
// 若未配置任何小程序凭据，返回 nil, nil，调用方据此判断微信能力未启用。
func NewManager(cfg config.WeChatConfig, cacheClient cache.Store, opts ...Option) (*Manager, error) {
	if !cfg.Declared() {
		return nil, nil //nolint:nilnil // returning nil manager when unconfigured is intentional
	}

	apps, err := cfg.Applications()
	if err != nil {
		return nil, fmt.Errorf("wechat: failed to parse applications config: %w", err)
	}

	m := &Manager{
		clients: make(map[string]MiniProgramClient, len(apps)),
		byAppID: make(map[string]MiniProgramClient, len(apps)),
	}

	for name, appCfg := range apps {
		client := NewClient(appCfg, cacheClient, opts...)
		if client == nil {
			continue
		}

		m.clients[name] = client
		m.byAppID[appCfg.AppID] = client

		if m.defaultApp == "" || name == DefaultAppKey {
			m.defaultApp = name
		}
	}

	return m, nil
}

// Default 返回默认的小程序客户端实例。
func (m *Manager) Default() (MiniProgramClient, error) {
	if m == nil {
		return nil, errs.ServiceUnavailable("wechat manager is not configured")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	client, ok := m.clients[m.defaultApp]
	if !ok || client == nil {
		return nil, errs.ServiceUnavailable("wechat default miniprogram is not configured")
	}

	return client, nil
}

// App 根据配置中的应用标识名称（如 "user"、"merchant" 或 "default"）检索对应的小程序客户端。
func (m *Manager) App(name string) (MiniProgramClient, error) {
	if m == nil {
		return nil, errs.ServiceUnavailable("wechat manager is not configured")
	}

	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed == DefaultAppKey {
		return m.Default()
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	client, ok := m.clients[trimmed]
	if !ok || client == nil {
		return nil, errs.NotFound(fmt.Sprintf("wechat miniprogram app %q not found", trimmed))
	}

	return client, nil
}

// AppByID 根据微信分配的实际 AppID（如 "wx123456..."）检索对应的小程序客户端。
func (m *Manager) AppByID(appID string) (MiniProgramClient, error) {
	if m == nil {
		return nil, errs.ServiceUnavailable("wechat manager is not configured")
	}

	trimmed := strings.TrimSpace(appID)
	if trimmed == "" {
		return m.Default()
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	client, ok := m.byAppID[trimmed]
	if !ok || client == nil {
		return nil, errs.NotFound(fmt.Sprintf("wechat miniprogram appid %q not found", trimmed))
	}

	return client, nil
}

// Register 动态注册或覆盖一个小程序客户端实例（主要用于扩展、自定义适配器或测试 Mock）。
func (m *Manager) Register(name string, client MiniProgramClient) {
	if m == nil || client == nil {
		return
	}

	key := cmp.Or(strings.TrimSpace(name), DefaultAppKey)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.clients[key] = client
	if client.AppID() != "" {
		m.byAppID[client.AppID()] = client
	}

	if m.defaultApp == "" || key == DefaultAppKey {
		m.defaultApp = key
	}
}

// Applications 返回当前已注册的全部小程序应用名称列表（按字母升序排序）。
func (m *Manager) Applications() []string {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	return slices.Sorted(maps.Keys(m.clients))
}

// Client 是微信小程序 API 客户端，直接实现 MiniProgramClient 及 AuthService / PhoneService / WxaCodeService 契约。
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

// WithBaseURL 覆盖默认域名，返回同一实例以便链式调用与单元测试起桩。
func (c *Client) WithBaseURL(baseURL string) *Client {
	WithBaseURL(baseURL)(c)
	return c
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
