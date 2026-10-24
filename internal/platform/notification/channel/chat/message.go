// Package chat 提供即时通讯（如 Telegram、飞书等）渠道与驱动实现。
package chat

// Message 表示标准的即时通讯通知载荷。
type Message struct {
	Recipient             string
	Text                  string
	ParseMode             string // "Markdown", "MarkdownV2", "HTML"
	DisableWebPagePreview bool
	DisableNotification   bool
}

// NewMessage 创建初始化为空的即时通讯消息对象。
func NewMessage() *Message {
	return &Message{}
}

// To 设置接收方标识（如 Telegram ChatID）。
func (m *Message) To(recipient string) *Message {
	m.Recipient = recipient
	return m
}

// WithText 设置消息正文文本。
func (m *Message) WithText(text string) *Message {
	m.Text = text
	return m
}

// WithParseMode 设置格式化解析模式。
func (m *Message) WithParseMode(mode string) *Message {
	m.ParseMode = mode
	return m
}

// Silent 设置静默投递。
func (m *Message) Silent() *Message {
	m.DisableNotification = true
	return m
}

// Notification 声明支持即时通讯格式化的通知接口契约。
type Notification interface {
	ToChat(notifiable any) any
}
