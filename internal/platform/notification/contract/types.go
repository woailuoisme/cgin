// Package contract 定义多渠道通知系统的核心抽象契约与参数。
package contract

import (
	"context"
	"errors"
	"time"
)

// Notification 所有支持通过通知中心分发的领域事件通知接口。
type Notification interface {
	// Via 返回该通知支持/要求投递的目标渠道名称列表。
	Via(notifiable any) []string
}

// ChannelFormatter 允许通知对象针对特定渠道生成原生消息载荷。
type ChannelFormatter interface {
	Format(channel string, notifiable any) any
}

// Channel 定义通知渠道契约。
type Channel interface {
	// Name 返回渠道标识名（如 "mail", "sms", "database", "telegram" 等）。
	Name() string

	// Send 将格式化后的具体载荷投递给接收方实体。
	Send(ctx context.Context, notifiable any, message any) error
}

// Formatter 可选由 Channel 实现，用于将抽象 Notification 转换为渠道专属消息。
type Formatter interface {
	Format(n Notification, notifiable any) (any, error)
}

// ShouldQueue 标识通知支持或建议通过后台异步队列分发。
type ShouldQueue interface {
	Notification
	ShouldQueue() bool
}

// ShouldQueueWithOptions 自定义通知入队的队列名称、重试与延迟参数。
type ShouldQueueWithOptions interface {
	ShouldQueue
	QueueName() string
	MaxRetries() int
	Delay() time.Duration
}

// IdempotentNotification 声明具备幂等防重键的通知契约。
type IdempotentNotification interface {
	Notification
	// IdempotentKey 返回该通知针对指定接收者的全局唯一防重键。
	IdempotentKey(notifiable any) string
}

// RateLimitedNotification 允许单个通知自声明频控规则。
type RateLimitedNotification interface {
	Notification
	// RateLimitKey 返回用于频控的唯一键；若返回空串则由中间件使用默认规则。
	RateLimitKey(channel string, notifiable any) string
}

// 标准哨兵错误定义。
var (
	ErrNoChannel      = errors.New("notification: channel not registered")
	ErrInvalidMessage = errors.New("notification: invalid message type for channel")
	ErrNoRoute        = errors.New("notification: notifiable has no route for channel")
	ErrNoFormatter    = errors.New("notification: notification has no formatter for channel")
	ErrClosed         = errors.New("notification: notifier closed")
	ErrRateLimited    = errors.New("notification: rate limit exceeded")
	ErrDeduplicated   = errors.New("notification: duplicate notification ignored")
)

// SendOption 定义单次 Send 调用的 Functional Option。
type SendOption func(*SendOptions)

// SendOptions 单次发送参数聚合结构体。
type SendOptions struct {
	Channels []string
	Sync     bool
	SyncSet  bool
}

// Via 在单次发送时覆盖 Notification.Via 所声明的渠道列表。
func Via(channels ...string) SendOption {
	return func(o *SendOptions) {
		o.Channels = channels
	}
}

// Sync 强制该次通知同步阻塞执行，常用于手机/邮箱验证码等关键路径。
func Sync() SendOption {
	return func(o *SendOptions) {
		o.Sync = true
		o.SyncSet = true
	}
}

// Async 强制该次通知异步派发进入后台 Worker 池。
func Async() SendOption {
	return func(o *SendOptions) {
		o.Sync = false
		o.SyncSet = true
	}
}
