// Package realtime 提供基于 Centrifugo 的高可用实时通信基础设施驱动与协议定义。
package realtime

import (
	"encoding/json"
)

// EventMessage 结构化实时事件载荷，消除 map[string]any 动态堆分配。
type EventMessage struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

// Centrifugo Proxy 错误码常量定义。
const (
	// ProxyErrorCodePermissionDenied 表示频道订阅权限不足。
	ProxyErrorCodePermissionDenied uint32 = 103
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
