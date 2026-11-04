package notification

import (
	"bytes"
	"cmp"
	"context"
	"slices"
	"sync"

	"github.com/samber/lo"
	"github.com/samber/oops"

	"cgin/pkg/errs"
	"cgin/pkg/mailer"
)

// MailAttachment 表示邮件通知载荷附件。
type MailAttachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// MailMessage 表示标准的邮件通知载荷。
type MailMessage struct {
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
	Attachments []MailAttachment
}

// NewMailMessage 创建初始化为空的邮件消息对象。
func NewMailMessage() *MailMessage {
	return &MailMessage{
		Headers: make(map[string]string),
	}
}

// From 设置发件人地址与发件人姓名。
func (m *MailMessage) From(address string, name ...string) *MailMessage {
	m.FromAddress = address
	if len(name) > 0 {
		m.FromName = name[0]
	}
	return m
}

// AddTo 追加收件人邮箱地址。
func (m *MailMessage) AddTo(to ...string) *MailMessage {
	m.To = append(m.To, to...)
	return m
}

// WithTo 设置/追加收件人邮箱地址。
func (m *MailMessage) WithTo(to ...string) *MailMessage {
	m.To = append(m.To, to...)
	return m
}

// AddCc 追加抄送邮箱地址。
func (m *MailMessage) AddCc(cc ...string) *MailMessage {
	m.Cc = append(m.Cc, cc...)
	return m
}

// AddBcc 追加密送邮箱地址。
func (m *MailMessage) AddBcc(bcc ...string) *MailMessage {
	m.Bcc = append(m.Bcc, bcc...)
	return m
}

// WithSubject 设置邮件主题。
func (m *MailMessage) WithSubject(subject string) *MailMessage {
	m.Subject = subject
	return m
}

// WithText 设置纯文本格式正文。
func (m *MailMessage) WithText(text string) *MailMessage {
	m.Text = text
	return m
}

// WithHTML 设置 HTML 格式正文。
func (m *MailMessage) WithHTML(html string) *MailMessage {
	m.HTML = html
	return m
}

// WithReplyTo 设置回复邮箱。
func (m *MailMessage) WithReplyTo(replyTo string) *MailMessage {
	m.ReplyTo = replyTo
	return m
}

// WithHeader 添加自定义邮件头。
func (m *MailMessage) WithHeader(key, value string) *MailMessage {
	if m.Headers == nil {
		m.Headers = make(map[string]string)
	}
	m.Headers[key] = value
	return m
}

// Attach 添加二进制邮件附件。
func (m *MailMessage) Attach(filename, contentType string, content []byte) *MailMessage {
	m.Attachments = append(m.Attachments, MailAttachment{
		Filename:    filename,
		ContentType: contentType,
		Content:     bytes.Clone(content),
	})
	return m
}

// MailNotification 声明支持邮件格式化的通知接口契约。
type MailNotification interface {
	ToMail(notifiable any) any
}

// MailDriver 定义底层邮件发送驱动接口。
type MailDriver interface {
	Name() string
	Send(ctx context.Context, msg *MailMessage) error
}

// MailChannel 邮件通知渠道实现，负责将通知渲染并分发给底层 Mailer 发送。
type MailChannel struct {
	mu            sync.RWMutex
	mailer        mailer.Mailer
	drivers       map[string]MailDriver
	defaultDriver string
}

var (
	_ Channel   = (*MailChannel)(nil)
	_ Formatter = (*MailChannel)(nil)
)

// NewMailChannel 创建邮件通知渠道实例。
func NewMailChannel(m mailer.Mailer) *MailChannel {
	return &MailChannel{
		mailer:  m,
		drivers: make(map[string]MailDriver),
	}
}

// Name 返回渠道标识名。
func (c *MailChannel) Name() string {
	return "mail"
}

// Mailer 返回当前配置的邮件发送器实例。
func (c *MailChannel) Mailer() mailer.Mailer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mailer
}

// RegisterDriver 注册指定名称的自定义邮件传输驱动（用于扩展或测试）。
func (c *MailChannel) RegisterDriver(name string, driver MailDriver) *MailChannel {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drivers[name] = driver
	if c.defaultDriver == "" {
		c.defaultDriver = name
	}
	return c
}

// Driver 获取指定驱动实例，默认返回配置的默认驱动。
func (c *MailChannel) Driver(name ...string) MailDriver {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.drivers[cmp.Or(lo.FirstOr(name, ""), c.defaultDriver)]
}

// Format 将抽象领域通知转换为具体邮件载荷。
func (c *MailChannel) Format(n Notification, notifiable any) (any, error) {
	if mailNotif, ok := n.(MailNotification); ok {
		return mailNotif.ToMail(notifiable), nil
	}

	if cf, ok := n.(ChannelFormatter); ok {
		if raw := cf.Format(c.Name(), notifiable); raw != nil {
			return raw, nil
		}
	}

	return nil, oops.Code(errs.CodeNotificationUnsupportedMail).
		With("notification", n).
		Errorf("notification does not implement MailNotification or ChannelFormatter")
}

// Send 执行邮件渠道的发送逻辑。
func (c *MailChannel) Send(ctx context.Context, notifiable any, message any) error {
	if notif, ok := message.(Notification); ok {
		formatted, err := c.Format(notif, notifiable)
		if err != nil {
			return err
		}
		message = formatted
	}

	recipientEmail := resolveMailRecipient(notifiable)

	if mailable, ok := message.(mailer.Mailable); ok {
		return c.sendMailable(ctx, recipientEmail, mailable)
	}

	if msg, ok := toPlatformMailMessage(message); ok {
		return c.sendDirectMessage(ctx, recipientEmail, msg)
	}

	return oops.Code(errs.CodeBadRequest).Errorf("mail: unsupported mail message payload type %T", message)
}

func (c *MailChannel) sendMailable(ctx context.Context, recipientEmail string, mailable mailer.Mailable) error {
	if recipientEmail == "" {
		return oops.Code(errs.CodeMailMissingRecipient).Errorf("mail: could not determine email address for notifiable")
	}
	if c.mailer != nil {
		return c.mailer.SendMailable(ctx, recipientEmail, mailable)
	}
	return oops.Code(errs.CodeMailDriverNotFound).Errorf("mail: mailer is not configured")
}

func (c *MailChannel) sendDirectMessage(ctx context.Context, recipientEmail string, msg *mailer.Message) error {
	if len(msg.To) == 0 && recipientEmail != "" {
		msg.To = []string{recipientEmail}
	}
	if len(msg.To) == 0 {
		return oops.Code(errs.CodeMailMissingRecipient).Errorf("mail: could not determine email address for notifiable")
	}
	if c.mailer != nil {
		return c.mailer.Send(ctx, *msg)
	}
	if drv := c.Driver(); drv != nil {
		return drv.Send(ctx, toChannelMailMessage(msg))
	}
	return oops.Code(errs.CodeMailDriverNotFound).Errorf("mail: mailer is not configured")
}

func resolveMailRecipient(notifiable any) string {
	if notifiable == nil {
		return ""
	}
	target := ToNotifiable(notifiable)
	if target == nil {
		return ""
	}
	return target.RouteNotificationFor("mail")
}

func toPlatformMailMessage(raw any) (*mailer.Message, bool) {
	switch v := raw.(type) {
	case *mailer.Message:
		return v, true
	case mailer.Message:
		return &v, true
	case *MailMessage:
		msg := &mailer.Message{
			To:      slices.Clone(v.To),
			Cc:      slices.Clone(v.Cc),
			Bcc:     slices.Clone(v.Bcc),
			Subject: v.Subject,
			Body:    v.Text,
		}
		if v.HTML != "" {
			msg.Body = v.HTML
			msg.IsHTML = true
		}
		return msg, true
	case MailMessage:
		return toPlatformMailMessage(&v)
	case string:
		return &mailer.Message{
			Subject: "Notification",
			Body:    v,
		}, true
	default:
		return nil, false
	}
}

func toChannelMailMessage(msg *mailer.Message) *MailMessage {
	m := NewMailMessage().
		WithSubject(msg.Subject).
		AddCc(msg.Cc...).
		AddBcc(msg.Bcc...)
	if len(msg.To) > 0 {
		m.WithTo(msg.To...)
	}
	if msg.IsHTML {
		m.WithHTML(msg.Body)
	} else {
		m.WithText(msg.Body)
	}
	return m
}
