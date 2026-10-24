// Package realtime 提供基于 Centrifugo 的高可用实时通信基础设施驱动与协议定义。
package realtime

import (
	"context"
	"time"
)

// Publisher 定义高并发实时事件发布与广播角色接口（单一职责原则 SRP）。
//
// 所有发送方法都要求显式传入 Context：出站调用必须能被调用方的取消与超时中断。
type Publisher interface {
	// DispatchContext 向单个频道推送事件消息。
	DispatchContext(ctx context.Context, channel, event string, data any) error
	// BroadcastContext 批量向多个频道推送同一事件消息。
	BroadcastContext(ctx context.Context, channels []string, event string, data any) error
	// PublishRaw 向单个频道直接发布原始字节数据（规避二次 JSON 序列化开销）。
	PublishRaw(ctx context.Context, channel string, data []byte) error
	// BroadcastRaw 批量向多个频道直接发布原始字节数据。
	BroadcastRaw(ctx context.Context, channels []string, data []byte) error
}

// TokenIssuer 定义 Centrifugo 客户端连接与频道订阅凭证签发角色接口。
type TokenIssuer interface {
	// GenerateConnectionToken 为指定用户签发客户端连接凭证（JWT HMAC-SHA256）。
	GenerateConnectionToken(userID string, expireAt time.Time, info, meta map[string]any, channels ...string) (string, error)
	// GenerateSubscriptionToken 为特定频道签发客户端订阅凭证（支持免 Webhook 高性能直连）。
	GenerateSubscriptionToken(userID, channel string, expireAt time.Time, opts ...SubscriptionTokenOption) (string, error)
}

// PresenceReader 定义频道在线成员状态与历史消息查询角色接口。
type PresenceReader interface {
	// History 查询指定频道的历史消息记录。
	History(ctx context.Context, channel string, limit int) ([]Publication, error)
	// Presence 查询指定频道的在线成员信息列表。
	Presence(ctx context.Context, channel string) (map[string]ClientInfo, error)
	// PresenceStats 查询指定频道的成员在线统计数据。
	PresenceStats(ctx context.Context, channel string) (PresenceStatsResult, error)
}

// ConnectionController 定义客户端连接断开与强制退订角色接口。
type ConnectionController interface {
	// Disconnect 强制断开指定用户的活跃连接。
	Disconnect(ctx context.Context, userID string) error
	// Unsubscribe 强制取消指定用户在特定频道的订阅。
	Unsubscribe(ctx context.Context, userID, channel string) error
}

// RealtimeGateway 为实时通信基础设施的统一抽象门面（聚合原子角色接口）。
type RealtimeGateway interface {
	Publisher
	TokenIssuer
	PresenceReader
	ConnectionController

	// Registry 返回频道订阅授权与发布策略注册中心。
	Registry() *Registry
	// Ping 探测底层实时服务连通性（满足 ready.Pinger 契约）。
	Ping(ctx context.Context) error
	// Enabled 报告实时通信服务是否处于启用状态。
	Enabled() bool
	// Driver 返回当前底层驱动标识（如 "centrifugo"）。
	Driver() string
}

// Gateway 是实时通信统一门面的类型别名，调用方一律依赖 Gateway 表达实时能力。
type Gateway = Manager

// Manager 维持完整实时通信管理器接口契约（向后兼容）。
type Manager interface {
	RealtimeGateway

	// Token 为 GenerateConnectionToken 的便捷别名。
	Token(userID string, expireAt time.Time, info, meta map[string]any, channels ...string) (string, error)
	// GetNodeID 返回节点标识。
	GetNodeID() string
	// Client 导出底层客户端连接指针（不推荐跨层直接使用）。
	Client() *Client
	// Config 返回基础设施配置。
	Config() *Config
}
