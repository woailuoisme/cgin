// Package chat 提供即时通讯（如 Telegram、飞书等）渠道与驱动实现。
package chat

import (
	"cmp"
	"context"
	"sync"

	"github.com/samber/lo"
	"github.com/samber/oops"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/notification/contract"
)

// Channel 即时通讯通知渠道实现。
type Channel struct {
	mu            sync.RWMutex
	drivers       map[string]Driver
	defaultDriver string
}

var _ contract.Channel = (*Channel)(nil)

// NewChannel 创建即时通讯通知渠道实例。
func NewChannel(defaultDriver string) *Channel {
	return &Channel{
		drivers:       make(map[string]Driver),
		defaultDriver: defaultDriver,
	}
}

// Name 返回渠道标识名。
func (c *Channel) Name() string {
	return "chat"
}

// RegisterDriver 注册即时通讯底层传输驱动。
func (c *Channel) RegisterDriver(name string, driver Driver) *Channel {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drivers[name] = driver
	if c.defaultDriver == "" {
		c.defaultDriver = name
	}
	return c
}

// Driver 获取指定驱动实例。
func (c *Channel) Driver(name ...string) Driver {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.drivers[cmp.Or(lo.FirstOr(name, ""), c.defaultDriver)]
}

// Format 将领域通知对象转换为即时通讯载荷 *Message。
func (c *Channel) Format(n contract.Notification, notifiable any) (any, error) {
	if chatNotif, ok := n.(Notification); ok {
		raw := chatNotif.ToChat(notifiable)
		return toChatMessage(raw)
	}

	if cf, ok := n.(contract.ChannelFormatter); ok {
		raw := cf.Format(c.Name(), notifiable)
		if raw != nil {
			return toChatMessage(raw)
		}
	}

	return nil, oops.Code(errs.CodeNotificationUnsupportedChat).
		With("notification", n).
		Errorf("notification does not implement chat.Notification or ChannelFormatter")
}

func toChatMessage(raw any) (*Message, error) {
	switch v := raw.(type) {
	case *Message:
		return v, nil
	case Message:
		return &v, nil
	case string:
		msg := NewMessage().WithText(v)
		return msg, nil
	default:
		return nil, oops.Code(errs.CodeInvalidChatMessage).Errorf("unsupported chat message payload type: %T", raw)
	}
}

// Send 执行即时通讯渠道的通知发送逻辑。
func (c *Channel) Send(ctx context.Context, notifiable any, message any) error {
	msg, ok := message.(*Message)
	if !ok {
		return oops.Code(errs.CodeBadRequest).Errorf("chat: expected *chat.Message, got %T", message)
	}

	if msg.Recipient == "" {
		if recipient := resolveRecipient(notifiable); recipient != "" {
			msg.To(recipient)
		}
	}

	driver := c.Driver()
	if driver == nil {
		return oops.Code(errs.CodeChatDriverNotFound).Errorf("chat: default driver %s is not registered", c.defaultDriver)
	}

	return driver.Send(ctx, msg)
}

// resolveRecipient 解析通知对象的即时通讯接收方标识。
func resolveRecipient(notifiable any) string {
	if notifiable == nil {
		return ""
	}
	target := contract.ToNotifiable(notifiable)
	if target == nil {
		return ""
	}
	if recipient := target.RouteNotificationFor("chat"); recipient != "" {
		return recipient
	}
	return target.RouteNotificationFor("telegram")
}
