// Package gotify 提供 Gotify 实时推送通知渠道与驱动实现。
package gotify

import (
	"maps"

	"github.com/samber/lo"
)

const (
	// DefaultPriority Gotify 默认通知优先级。
	DefaultPriority = 5
	// PriorityMin 最低优先级（静默消息，无声音提示）。
	PriorityMin = 0
	// PriorityLow 低优先级。
	PriorityLow = 2
	// PriorityNormal 标准优先级。
	PriorityNormal = 5
	// PriorityHigh 高优先级（振动与通知栏醒目展示）。
	PriorityHigh = 8
	// PriorityUrgent 紧急最高优先级。
	PriorityUrgent = 10

	// ExtraKeyClientDisplay Gotify 客户端展示格式扩展键。
	ExtraKeyClientDisplay = "client::display"
	// ExtraKeyClientNotification Gotify 客户端交互通知扩展键。
	ExtraKeyClientNotification = "client::notification"
	// ContentTypeMarkdown Markdown 内容类型。
	ContentTypeMarkdown = "text/markdown"
)

// Message 表示标准的 Gotify 推送通知载荷。
type Message struct {
	Title     string
	Message   string
	Priority  int
	Extras    map[string]any
	Token     string
	ServerURL string
}

// NewMessage 创建初始化默认优先级的 Gotify 消息对象。
func NewMessage(msg ...string) *Message {
	return &Message{
		Message:  lo.FirstOr(msg, ""),
		Priority: DefaultPriority,
		Extras:   make(map[string]any),
	}
}

// WithTitle 设置消息标题。
func (m *Message) WithTitle(title string) *Message {
	m.Title = title
	return m
}

// WithMessage 设置消息正文文本。
func (m *Message) WithMessage(msg string) *Message {
	m.Message = msg
	return m
}

// WithPriority 设置消息优先级 (0-10)。
func (m *Message) WithPriority(p int) *Message {
	m.Priority = p
	return m
}

// UrgentPriority 设置紧急最高优先级 (10)。
func (m *Message) UrgentPriority() *Message {
	m.Priority = PriorityUrgent
	return m
}

// HighPriority 设置高优先级 (8)。
func (m *Message) HighPriority() *Message {
	m.Priority = PriorityHigh
	return m
}

// NormalPriority 设置普通优先级 (5)。
func (m *Message) NormalPriority() *Message {
	m.Priority = PriorityNormal
	return m
}

// LowPriority 设置低优先级 (2)。
func (m *Message) LowPriority() *Message {
	m.Priority = PriorityLow
	return m
}

// MinPriority 设置最低优先级 (0)。
func (m *Message) MinPriority() *Message {
	m.Priority = PriorityMin
	return m
}

// Markdown 开启 Markdown 渲染展示。
func (m *Message) Markdown() *Message {
	return m.WithMarkdown(true)
}

// WithMarkdown 控制是否开启 Markdown 渲染展示。
func (m *Message) WithMarkdown(enable bool) *Message {
	if m.Extras == nil {
		m.Extras = make(map[string]any)
	}
	if enable {
		m.Extras[ExtraKeyClientDisplay] = map[string]any{
			"contentType": ContentTypeMarkdown,
		}
	} else {
		delete(m.Extras, ExtraKeyClientDisplay)
	}
	return m
}

// WithClickURL 设置点击通知时的外部跳转链接。
func (m *Message) WithClickURL(url string) *Message {
	if m.Extras == nil {
		m.Extras = make(map[string]any)
	}
	if url != "" {
		m.Extras[ExtraKeyClientNotification] = map[string]any{
			"click": map[string]any{
				"url": url,
			},
		}
	} else {
		delete(m.Extras, ExtraKeyClientNotification)
	}
	return m
}

// WithExtra 设置自定义扩展属性键值。
func (m *Message) WithExtra(key string, val any) *Message {
	if m.Extras == nil {
		m.Extras = make(map[string]any)
	}
	m.Extras[key] = val
	return m
}

// WithExtras 批量合并扩展属性。
func (m *Message) WithExtras(extras map[string]any) *Message {
	if m.Extras == nil {
		m.Extras = make(map[string]any)
	}
	maps.Copy(m.Extras, extras)
	return m
}

// WithToken 显式指定本次推送的应用 AppToken。
func (m *Message) WithToken(token string) *Message {
	m.Token = token
	return m
}

// WithServerURL 显式指定本次推送的目标服务器地址。
func (m *Message) WithServerURL(url string) *Message {
	m.ServerURL = url
	return m
}

// Notification 声明支持 Gotify 格式化的领域通知接口契约。
type Notification interface {
	ToGotify(notifiable any) any
}
