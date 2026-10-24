package wechat

import (
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

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
