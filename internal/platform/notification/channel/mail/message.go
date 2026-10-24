// Package mail 提供邮件通知渠道与驱动实现。
package mail

// Attachment 表示邮件附件。
type Attachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// Message 表示标准的邮件通知载荷。
type Message struct {
	FromAddress string
	FromName    string
	To          []string
	Cc          []string
	Bcc         []string
	ReplyTo     string
	Subject     string
	Text        string
	HTML        string
	Headers     map[string]string
	Attachments []Attachment
}

// NewMessage 创建初始化为空的邮件消息对象。
func NewMessage() *Message {
	return &Message{
		To:          make([]string, 0),
		Cc:          make([]string, 0),
		Bcc:         make([]string, 0),
		Headers:     make(map[string]string),
		Attachments: make([]Attachment, 0),
	}
}

// From 设置发件人地址与发件人姓名。
func (m *Message) From(address string, name ...string) *Message {
	m.FromAddress = address
	if len(name) > 0 {
		m.FromName = name[0]
	}
	return m
}

// AddTo 追加收件人邮箱地址。
func (m *Message) AddTo(to ...string) *Message {
	m.To = append(m.To, to...)
	return m
}

// WithTo 设置/追加收件人邮箱地址。
func (m *Message) WithTo(to ...string) *Message {
	m.To = append(m.To, to...)
	return m
}

// AddCc 追加抄送邮箱地址。
func (m *Message) AddCc(cc ...string) *Message {
	m.Cc = append(m.Cc, cc...)
	return m
}

// AddBcc 追加密送邮箱地址。
func (m *Message) AddBcc(bcc ...string) *Message {
	m.Bcc = append(m.Bcc, bcc...)
	return m
}

// WithSubject 设置邮件主题。
func (m *Message) WithSubject(subject string) *Message {
	m.Subject = subject
	return m
}

// WithText 设置纯文本格式正文。
func (m *Message) WithText(text string) *Message {
	m.Text = text
	return m
}

// WithHTML 设置 HTML 格式正文。
func (m *Message) WithHTML(html string) *Message {
	m.HTML = html
	return m
}

// WithReplyTo 设置回复邮箱。
func (m *Message) WithReplyTo(replyTo string) *Message {
	m.ReplyTo = replyTo
	return m
}

// WithHeader 添加自定义邮件头。
func (m *Message) WithHeader(key, value string) *Message {
	if m.Headers == nil {
		m.Headers = make(map[string]string)
	}
	m.Headers[key] = value
	return m
}

// Attach 添加二进制邮件附件。
func (m *Message) Attach(filename, contentType string, content []byte) *Message {
	m.Attachments = append(m.Attachments, Attachment{
		Filename:    filename,
		ContentType: contentType,
		Content:     content,
	})
	return m
}

// Notification 声明支持邮件格式化的通知接口契约。
type Notification interface {
	ToMail(notifiable any) any
}
