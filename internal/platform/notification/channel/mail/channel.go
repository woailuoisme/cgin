// Package mail 提供邮件通知渠道与驱动实现。
package mail

import (
	"cmp"
	"context"
	"sync"

	"github.com/samber/lo"
	"github.com/samber/oops"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/mailer"
	"bgin/internal/platform/notification/contract"
)

// Channel 邮件通知渠道实现，负责将通知渲染并分发给底层 Mailer 发送。
type Channel struct {
	mu            sync.RWMutex
	mailer        mailer.Mailer
	drivers       map[string]Driver
	defaultDriver string
}

var _ contract.Channel = (*Channel)(nil)

// NewChannel 创建邮件通知渠道实例。
func NewChannel(m mailer.Mailer) *Channel {
	return &Channel{
		mailer:  m,
		drivers: make(map[string]Driver),
	}
}

// Name 返回渠道标识名。
func (c *Channel) Name() string {
	return "mail"
}

// Mailer 返回当前配置的邮件发送器实例。
func (c *Channel) Mailer() mailer.Mailer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mailer
}

// RegisterDriver 注册指定名称的自定义邮件传输驱动（用于扩展或测试）。
func (c *Channel) RegisterDriver(name string, driver Driver) *Channel {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drivers[name] = driver
	if c.defaultDriver == "" {
		c.defaultDriver = name
	}
	return c
}

// Driver 获取指定驱动实例，默认返回配置的默认驱动。
func (c *Channel) Driver(name ...string) Driver {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.drivers[cmp.Or(lo.FirstOr(name, ""), c.defaultDriver)]
}

// Format 将抽象领域通知转换为具体邮件载荷。
func (c *Channel) Format(n contract.Notification, notifiable any) (any, error) {
	if mailNotif, ok := n.(Notification); ok {
		raw := mailNotif.ToMail(notifiable)
		return raw, nil
	}

	if cf, ok := n.(contract.ChannelFormatter); ok {
		raw := cf.Format(c.Name(), notifiable)
		if raw != nil {
			return raw, nil
		}
	}

	return nil, oops.Code(errs.CodeNotificationUnsupportedMail).
		With("notification", n).
		Errorf("notification does not implement mail.Notification or ChannelFormatter")
}

// Send 执行邮件渠道的发送逻辑。
func (c *Channel) Send(ctx context.Context, notifiable any, message any) error {
	// 1. 若入参为 Notification，先执行 Format 转换为具体载荷
	if notif, ok := message.(contract.Notification); ok {
		formatted, err := c.Format(notif, notifiable)
		if err != nil {
			return err
		}
		message = formatted
	}

	// 2. 解析接收者邮箱地址
	recipientEmail := resolveRecipientEmail(notifiable)

	// 3. 处理 Mailable 结构化模板邮件
	if mailable, ok := message.(mailer.Mailable); ok {
		return c.sendMailable(ctx, recipientEmail, mailable)
	}

	// 4. 处理标准邮件载荷
	if msg, ok := toPlatformMailMessage(message); ok {
		return c.sendDirectMessage(ctx, recipientEmail, msg)
	}

	return oops.Code(errs.CodeBadRequest).Errorf("mail: unsupported mail message payload type %T", message)
}

// sendMailable 发送 Mailable 模板邮件。
func (c *Channel) sendMailable(ctx context.Context, recipientEmail string, mailable mailer.Mailable) error {
	if recipientEmail == "" {
		return oops.Code(errs.CodeMailMissingRecipient).Errorf("mail: could not determine email address for notifiable")
	}
	if c.mailer != nil {
		return c.mailer.SendMailable(ctx, recipientEmail, mailable)
	}
	return oops.Code(errs.CodeMailDriverNotFound).Errorf("mail: mailer is not configured")
}

// sendDirectMessage 发送直接构建的邮件消息。
func (c *Channel) sendDirectMessage(ctx context.Context, recipientEmail string, msg *mailer.Message) error {
	if msg.To == "" && recipientEmail != "" {
		msg.To = recipientEmail
	}
	if msg.To == "" {
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

// resolveRecipientEmail 解析通知对象的邮箱目标。
func resolveRecipientEmail(notifiable any) string {
	if notifiable == nil {
		return ""
	}
	target := contract.ToNotifiable(notifiable)
	if target == nil {
		return ""
	}
	return target.RouteNotificationFor("mail")
}

// toPlatformMailMessage 将任意邮件格式归一化为 *mailer.Message。
func toPlatformMailMessage(raw any) (*mailer.Message, bool) {
	switch v := raw.(type) {
	case *mailer.Message:
		return v, true
	case mailer.Message:
		return &v, true
	case *Message:
		to := ""
		if len(v.To) > 0 {
			to = v.To[0]
		}
		msg := &mailer.Message{
			To:      to,
			Cc:      v.Cc,
			Bcc:     v.Bcc,
			Subject: v.Subject,
			Body:    v.Text,
			IsHTML:  false,
		}
		if len(v.To) > 1 {
			msg.Cc = append(msg.Cc, v.To[1:]...)
		}
		if v.HTML != "" {
			msg.Body = v.HTML
			msg.IsHTML = true
		}
		return msg, true
	case Message:
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

// toChannelMailMessage 将 mailer.Message 转换为 channel/mail.Message。
func toChannelMailMessage(msg *mailer.Message) *Message {
	m := NewMessage().
		WithSubject(msg.Subject).
		AddCc(msg.Cc...).
		AddBcc(msg.Bcc...)
	if msg.To != "" {
		m.WithTo(msg.To)
	}
	if msg.IsHTML {
		m.WithHTML(msg.Body)
	} else {
		m.WithText(msg.Body)
	}
	return m
}
