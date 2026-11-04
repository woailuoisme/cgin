// Package notification 提供统一、模块化、高可扩展的多渠道通知基础设施。
package notification

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	// DefaultNotifiableType 默认接收者实体类型。
	DefaultNotifiableType = "users"
)

// authIdentifierGetter 认证标识提取接口（Duck-typing 结构化隔离）。
type authIdentifierGetter interface {
	GetAuthIdentifier() string
}

// idGetter 实体 ID 提取接口。
type idGetter interface {
	GetID() string
}

// authEmailGetter 认证邮箱提取接口。
type authEmailGetter interface {
	GetAuthEmail() string
}

// emailGetter 实体邮箱提取接口。
type emailGetter interface {
	GetEmail() string
}

// phoneGetter 实体手机号提取接口。
type phoneGetter interface {
	GetPhoneNumber() string
}

// mobileGetter 备用手机号提取接口。
type mobileGetter interface {
	GetMobile() string
}

// phoneStrGetter 备用简短手机号提取接口。
type phoneStrGetter interface {
	GetPhone() string
}

// Notifiable 表示支持接收多渠道通知的领域实体契约。
type Notifiable interface {
	// GetNotifiableID 返回接收者唯一标识符（如用户 ID）。
	GetNotifiableID() string

	// GetNotifiableType 返回接收者实体类型名（如 "users"）。
	GetNotifiableType() string

	// RouteNotificationFor 根据渠道名返回目标路由标识（如邮箱地址、手机号、ChatID 等）。
	RouteNotificationFor(channel string) string
}

// Recipient 具备灵活路由映射的通用通知接收者实现。
type Recipient struct {
	ID     string
	Type   string
	routes map[string]string
}

var _ Notifiable = (*Recipient)(nil)

// NewRecipient 创建具备指定 ID 与类型的通知接收者。
func NewRecipient(id string, notifiableType string) *Recipient {
	if notifiableType == "" {
		notifiableType = DefaultNotifiableType
	}
	return &Recipient{
		ID:     id,
		Type:   notifiableType,
		routes: make(map[string]string),
	}
}

// GetNotifiableID 返回接收者唯一标识符。
func (r *Recipient) GetNotifiableID() string {
	return r.ID
}

// GetNotifiableType 返回接收者实体类型名。
func (r *Recipient) GetNotifiableType() string {
	return r.Type
}

// RouteNotificationFor 获取指定渠道的路由标识。
func (r *Recipient) RouteNotificationFor(channel string) string {
	if r.routes == nil {
		return ""
	}
	return r.routes[channel]
}

// WithRoute 设置指定渠道的目标路由标识。
func (r *Recipient) WithRoute(channel string, target string) *Recipient {
	if r.routes == nil {
		r.routes = make(map[string]string)
	}
	r.routes[channel] = target
	return r
}

// WithEmail 设置邮件渠道目标地址。
func (r *Recipient) WithEmail(email string) *Recipient {
	return r.WithRoute("mail", email)
}

// WithPhone 设置短信渠道目标手机号。
func (r *Recipient) WithPhone(phone string) *Recipient {
	return r.WithRoute("sms", phone)
}

// WithChatID 设置聊天渠道目标会话 ID。
func (r *Recipient) WithChatID(chatID string) *Recipient {
	return r.WithRoute("chat", chatID).WithRoute("telegram", chatID)
}

// Route 快速构造仅包含单一渠道路由目标的临时接收者。
func Route(channel string, target string) Notifiable {
	return NewRecipient(target, "anonymous").WithRoute(channel, target)
}

// routeForMail 特化邮件路由方法检查接口。
type routeForMail interface {
	RouteNotificationForMail() string
}

// routeForSMS 特化短信路由方法检查接口。
type routeForSMS interface {
	RouteNotificationForSMS() string
}

// routeForChat 特化聊天会话路由方法检查接口。
type routeForChat interface {
	RouteNotificationForChat() string
}

// adaptedNotifiable 包装通用任意对象的适配器。
type adaptedNotifiable struct {
	raw any
}

var _ Notifiable = (*adaptedNotifiable)(nil)

func (a *adaptedNotifiable) GetNotifiableID() string {
	if a.raw == nil {
		return ""
	}
	if v, ok := a.raw.(authIdentifierGetter); ok {
		return v.GetAuthIdentifier()
	}
	if v, ok := a.raw.(idGetter); ok {
		return v.GetID()
	}
	switch v := a.raw.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	}
	return ""
}

func (a *adaptedNotifiable) GetNotifiableType() string {
	return DefaultNotifiableType
}

func (a *adaptedNotifiable) RouteNotificationFor(channel string) string {
	switch channel {
	case "mail":
		if res := routeMail(a.raw); res != "" {
			return res
		}
	case "sms":
		if res := routeSMS(a.raw); res != "" {
			return res
		}
	case "chat", "telegram":
		if r, ok := a.raw.(routeForChat); ok {
			return r.RouteNotificationForChat()
		}
	}
	if s, ok := a.raw.(string); ok {
		return s
	}
	return ""
}

// routeMail 解析针对邮件通知通道的目标地址。
func routeMail(raw any) string {
	if raw == nil {
		return ""
	}
	if r, ok := raw.(routeForMail); ok {
		return r.RouteNotificationForMail()
	}
	if v, ok := raw.(authEmailGetter); ok {
		return v.GetAuthEmail()
	}
	if v, ok := raw.(emailGetter); ok {
		return v.GetEmail()
	}
	return ""
}

// routeSMS 解析针对短信通知通道的目标手机号码。
func routeSMS(raw any) string {
	if raw == nil {
		return ""
	}
	if r, ok := raw.(routeForSMS); ok {
		return r.RouteNotificationForSMS()
	}
	if v, ok := raw.(phoneGetter); ok {
		return v.GetPhoneNumber()
	}
	if v, ok := raw.(mobileGetter); ok {
		return v.GetMobile()
	}
	if v, ok := raw.(phoneStrGetter); ok {
		return v.GetPhone()
	}
	return ""
}

// ToNotifiable 将任意实体对象安全转换为 Notifiable 契约实例。
func ToNotifiable(target any) Notifiable {
	if target == nil {
		return nil
	}
	if n, ok := target.(Notifiable); ok {
		return n
	}
	return &adaptedNotifiable{raw: target}
}

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
	// Name 返回渠道标识名（如 "mail", "sms", "database", "realtime" 等）。
	Name() string

	// Send 将格式化后的具体载荷投递给接收方实体。
	Send(ctx context.Context, notifiable any, message any) error
}

// BatchChannel 可选由 Channel 实现，支持将多个接收者的载荷合并为单次网络/存储往返批量投递（消除 N+1）。
type BatchChannel interface {
	Channel
	SendBatch(ctx context.Context, notifiables []any, messages []any) error
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
		o.Channels = slices.Clone(channels)
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
