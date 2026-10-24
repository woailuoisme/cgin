// Package sms 提供短消息通知渠道与多厂商驱动实现。
package sms

import (
	"cmp"
	"context"
	"sync"

	"github.com/samber/lo"
	"github.com/samber/oops"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/notification/contract"
)

// Channel 短信通知渠道实现。
type Channel struct {
	mu            sync.RWMutex
	drivers       map[string]Driver
	defaultDriver string
	signName      string
	templateCode  string
}

var _ contract.Channel = (*Channel)(nil)

// NewChannel 创建短信通知渠道实例。
func NewChannel(defaultDriver string) *Channel {
	return &Channel{
		drivers:       make(map[string]Driver),
		defaultDriver: defaultDriver,
	}
}

// WithDefaults 配置渠道默认短信签名与模板编码。
func (c *Channel) WithDefaults(signName, templateCode string) *Channel {
	c.signName = signName
	c.templateCode = templateCode
	return c
}

// Name 返回渠道标识名。
func (c *Channel) Name() string {
	return "sms"
}

// RegisterDriver 注册短信底层传输驱动。
func (c *Channel) RegisterDriver(name string, driver Driver) *Channel {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drivers[name] = driver
	if c.defaultDriver == "" {
		c.defaultDriver = name
	}
	return c
}

// Driver 获取指定驱动实例，若无传参则返回当前默认驱动。
func (c *Channel) Driver(name ...string) Driver {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.drivers[cmp.Or(lo.FirstOr(name, ""), c.defaultDriver)]
}

// Format 将领域通知对象转换为短信载荷 *Message。
func (c *Channel) Format(n contract.Notification, notifiable any) (any, error) {
	if smsNotif, ok := n.(Notification); ok {
		raw := smsNotif.ToSMS(notifiable)
		return toSMSMessage(raw)
	}

	if cf, ok := n.(contract.ChannelFormatter); ok {
		raw := cf.Format(c.Name(), notifiable)
		if raw != nil {
			return toSMSMessage(raw)
		}
	}

	return nil, oops.Code(errs.CodeNotificationUnsupportedSMS).
		With("notification", n).
		Errorf("notification does not implement sms.Notification or ChannelFormatter")
}

func toSMSMessage(raw any) (*Message, error) {
	switch v := raw.(type) {
	case *Message:
		return v, nil
	case Message:
		return &v, nil
	case string:
		msg := NewMessage().WithContent(v)
		return msg, nil
	default:
		return nil, oops.Code(errs.CodeInvalidSMSMessage).Errorf("unsupported sms message payload type: %T", raw)
	}
}

// Send 执行短信渠道的通知发送逻辑。
func (c *Channel) Send(ctx context.Context, notifiable any, message any) error {
	msg, ok := message.(*Message)
	if !ok {
		return oops.Code(errs.CodeBadRequest).Errorf("sms: expected *sms.Message, got %T", message)
	}

	if msg.PhoneNumber == "" && notifiable != nil {
		target := contract.ToNotifiable(notifiable)
		if target != nil {
			phone := target.RouteNotificationFor("sms")
			if phone != "" {
				msg.WithPhone(phone)
			}
		}
	}

	if msg.PhoneNumber == "" {
		return oops.Code(errs.CodeSMSMissingPhone).Errorf("sms: no phone number found for notifiable")
	}

	if msg.SignName == "" {
		msg.SignName = c.signName
	}
	if msg.TemplateCode == "" {
		msg.TemplateCode = c.templateCode
	}

	driver := c.Driver()
	if driver == nil {
		return oops.Code(errs.CodeSMSDriverNotFound).Errorf("sms: default driver %s is not registered", c.defaultDriver)
	}

	return driver.Send(ctx, msg)
}

// SendOTP 发送一次性短信验证码（便捷直发方法）。
func (c *Channel) SendOTP(ctx context.Context, phoneNumber string, code string) error {
	msg := NewMessage().
		WithPhone(phoneNumber).
		WithSignName(c.signName).
		WithTemplate(c.templateCode).
		WithParam("code", code).
		WithParam("otp", code).
		WithContent(code)

	driver := c.Driver()
	if driver == nil {
		return oops.Code(errs.CodeSMSDriverNotFound).Errorf("sms: default driver %s is not registered", c.defaultDriver)
	}

	return driver.Send(ctx, msg)
}
