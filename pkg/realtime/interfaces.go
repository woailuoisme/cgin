// Package realtime 提供基于 Centrifugo 的高可用实时通信基础设施驱动与协议定义。
package realtime

import (
	"context"
	"time"
)

// Publisher 是发布侧的最小契约：通知渠道等调用方只依赖它，不背上签发与巡检职责。
type Publisher interface {
	// DispatchContext 向单个频道推送事件消息（出站调用受调用方 Context 约束）。
	DispatchContext(ctx context.Context, channel, event string, data any) error
}

// Gateway 是后端服务与 Centrifugo 交互的唯一门面。
//
// 分工遵循 Centrifugo 的部署模型：连接管理与消息扇出由 Centrifugo 负责，
// 后端只承担三件事 —— 发布消息、签发连接凭据、代理订阅鉴权。
// Presence/History 等管理面能力属于运维直连 Centrifugo 的范畴，不在此重复包装。
type Gateway interface {
	Publisher
	// GenerateConnectionToken 为指定用户签发客户端连接凭证（JWT HMAC-SHA256）。
	GenerateConnectionToken(userID string, expireAt time.Time, info, meta map[string]any, channels ...string) (string, error)
	// Registry 返回频道订阅授权注册中心。
	Registry() *Registry
	// Ping 探测底层实时服务连通性（满足 ready.Pinger 契约）。
	Ping(ctx context.Context) error
	// Enabled 报告实时通信服务是否处于启用状态。
	Enabled() bool
	// Driver 返回底层驱动标识（满足巡检的可选能力契约）。
	Driver() string
}
