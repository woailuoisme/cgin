// Package realtime 提供基于 Centrifugo 的高可用实时通信基础设施驱动与协议定义。
package realtime

import (
	"encoding/json"
	"strings"

	"github.com/centrifugal/gocent/v3"
)

// Publication 代表频道历史消息记录项（解耦底层 gocent 类型）。
type Publication = gocent.Publication

// ClientInfo 代表频道活跃客户端信息（解耦底层 gocent 类型）。
type ClientInfo = gocent.ClientInfo

// PresenceStatsResult 代表在线统计聚合结果（解耦底层 gocent 类型）。
type PresenceStatsResult = gocent.PresenceStatsResult

// Client 代表底层实时传输客户端（解耦底层 gocent 类型）。
type Client = gocent.Client

// EventMessage 结构化实时事件载荷，消除 map[string]any 动态堆分配。
type EventMessage struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

// Centrifugo Proxy 错误码常量定义
const (
	// ProxyErrorCodeUnauthorized 表示未授权连接。
	ProxyErrorCodeUnauthorized uint32 = 1000
	// ProxyErrorCodePermissionDenied 表示频道订阅权限不足。
	ProxyErrorCodePermissionDenied uint32 = 103
	// ProxyErrorCodeBadRequest 表示代理请求格式或参数错误。
	ProxyErrorCodeBadRequest uint32 = 107
	// ProxyErrorCodeInternalError 表示内部处理异常。
	ProxyErrorCodeInternalError uint32 = 500
)

// ProxyError 定义 Centrifugo 约定的错误体。
type ProxyError struct {
	Code    uint32 `json:"code"`
	Message string `json:"message"`
}

// DisconnectCommand 定义 Centrifugo 强制断开连接指令。
type DisconnectCommand struct {
	Code   uint32 `json:"code"`
	Reason string `json:"reason"`
}

// User 表示实时通信中解析出的通用用户信息。
type User struct {
	ID        string         `json:"id"`
	Role      string         `json:"role,omitempty"`
	GuardName string         `json:"guardName,omitempty"`
	Info      map[string]any `json:"info,omitempty"`
}

// ConnectRequest 定义 Centrifugo Proxy Connect 发送到后端的请求体。
type ConnectRequest struct {
	Client    string          `json:"client"`
	Transport string          `json:"transport"`
	Protocol  string          `json:"protocol"`
	Encoding  string          `json:"encoding"`
	Data      json.RawMessage `json:"data,omitempty"`
	Headers   map[string]any  `json:"headers,omitempty"`
}

// GetHeader 从 Headers 映射中不区分大小写地安全提取单个标头字串。
func (r *ConnectRequest) GetHeader(key string) string {
	for k, v := range r.Headers {
		if strings.EqualFold(k, key) {
			return extractHeaderValue(v)
		}
	}

	return ""
}

func extractHeaderValue(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []string:
		if len(val) > 0 {
			return val[0]
		}
	case []any:
		if len(val) == 0 {
			return ""
		}
		if str, ok := val[0].(string); ok {
			return str
		}
	}
	return ""
}

// ConnectResponse 定义返回给 Centrifugo 的 Connect 响应包装。
type ConnectResponse struct {
	Result     *ConnectResult     `json:"result,omitempty"`
	Error      *ProxyError        `json:"error,omitempty"`
	Disconnect *DisconnectCommand `json:"disconnect,omitempty"`
}

// ConnectResult 包含成功的连接元数据。
type ConnectResult struct {
	User     string         `json:"user"`
	ExpireAt int64          `json:"expire_at,omitempty"`
	Info     map[string]any `json:"info,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
	Data     any            `json:"data,omitempty"`
	Channels []string       `json:"channels,omitempty"`
}

// RefreshRequest 定义 Centrifugo Proxy Refresh 发送到后端的会话刷新请求体。
type RefreshRequest struct {
	Client    string         `json:"client"`
	Transport string         `json:"transport"`
	Protocol  string         `json:"protocol"`
	Encoding  string         `json:"encoding"`
	User      string         `json:"user"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// RefreshResponse 定义返回给 Centrifugo 的 Refresh 响应包装。
type RefreshResponse struct {
	Result     *RefreshResult     `json:"result,omitempty"`
	Error      *ProxyError        `json:"error,omitempty"`
	Disconnect *DisconnectCommand `json:"disconnect,omitempty"`
}

// RefreshResult 包含会话刷新成功的元数据。
type RefreshResult struct {
	ExpireAt int64          `json:"expire_at,omitempty"`
	Info     map[string]any `json:"info,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}

// SubscribeRequest 定义 Centrifugo Proxy Subscribe 发送到后端的请求体。
type SubscribeRequest struct {
	Client    string          `json:"client"`
	Transport string          `json:"transport"`
	Protocol  string          `json:"protocol"`
	Encoding  string          `json:"encoding"`
	User      string          `json:"user"`
	Channel   string          `json:"channel"`
	Data      json.RawMessage `json:"data,omitempty"`
	Meta      map[string]any  `json:"meta,omitempty"`
	Headers   map[string]any  `json:"headers,omitempty"`
}

// SubscribeResponse 定义返回给 Centrifugo 的 Subscribe 响应包装。
type SubscribeResponse struct {
	Result     *SubscribeResult   `json:"result,omitempty"`
	Error      *ProxyError        `json:"error,omitempty"`
	Disconnect *DisconnectCommand `json:"disconnect,omitempty"`
}

// SubscribeResult 包含成功的订阅元数据。
type SubscribeResult struct {
	ExpireAt int64          `json:"expire_at,omitempty"`
	Info     map[string]any `json:"info,omitempty"`
	Data     any            `json:"data,omitempty"`
}

// SubRefreshRequest 定义 Centrifugo Proxy SubRefresh 发送到后端的订阅刷新请求体。
type SubRefreshRequest struct {
	Client    string         `json:"client"`
	Transport string         `json:"transport"`
	Protocol  string         `json:"protocol"`
	Encoding  string         `json:"encoding"`
	User      string         `json:"user"`
	Channel   string         `json:"channel"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// SubRefreshResponse 定义返回给 Centrifugo 的 SubRefresh 响应包装。
type SubRefreshResponse struct {
	Result     *SubRefreshResult  `json:"result,omitempty"`
	Error      *ProxyError        `json:"error,omitempty"`
	Disconnect *DisconnectCommand `json:"disconnect,omitempty"`
}

// SubRefreshResult 包含订阅刷新成功的元数据。
type SubRefreshResult struct {
	ExpireAt int64          `json:"expire_at,omitempty"`
	Info     map[string]any `json:"info,omitempty"`
}

// PublishRequest 定义 Centrifugo Proxy Publish 发送到后端的客户端直接发布请求体。
type PublishRequest struct {
	Client    string          `json:"client"`
	Transport string          `json:"transport"`
	Protocol  string          `json:"protocol"`
	Encoding  string          `json:"encoding"`
	User      string          `json:"user"`
	Channel   string          `json:"channel"`
	Data      json.RawMessage `json:"data"`
	Meta      map[string]any  `json:"meta,omitempty"`
}

// PublishResponse 定义返回给 Centrifugo 的 Publish 响应包装。
type PublishResponse struct {
	Result     *PublishResult     `json:"result,omitempty"`
	Error      *ProxyError        `json:"error,omitempty"`
	Disconnect *DisconnectCommand `json:"disconnect,omitempty"`
}

// PublishResult 包含发布响应数据。
type PublishResult struct {
	Data any `json:"data,omitempty"`
}

// RPCRequest 定义 Centrifugo Proxy RPC 发送到后端的远程调用请求体。
type RPCRequest struct {
	Client    string          `json:"client"`
	Transport string          `json:"transport"`
	Protocol  string          `json:"protocol"`
	Encoding  string          `json:"encoding"`
	User      string          `json:"user"`
	Method    string          `json:"method"`
	Data      json.RawMessage `json:"data,omitempty"`
	Meta      map[string]any  `json:"meta,omitempty"`
}

// RPCResponse 定义返回给 Centrifugo 的 RPC 响应包装。
type RPCResponse struct {
	Result     *RPCResult         `json:"result,omitempty"`
	Error      *ProxyError        `json:"error,omitempty"`
	Disconnect *DisconnectCommand `json:"disconnect,omitempty"`
}

// RPCResult 包含 RPC 调用的返回数据。
type RPCResult struct {
	Data any `json:"data,omitempty"`
}
