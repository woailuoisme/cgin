// Package realtime 提供基于 WebSocket / Centrifugo 的实时事件广播通知渠道实现。
package realtime

import (
	"context"

	"github.com/samber/oops"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/notification/contract"
	"bgin/internal/platform/realtime"
)

// Message 表示实时广播通知载荷。
type Message struct {
	Channel string `json:"channel"`
	Event   string `json:"event"`
	Data    any    `json:"data"`
}

// Notification 声明支持实时广播的通知接口契约。
type Notification interface {
	ToRealtime(notifiable any) Message
}

// Channel 实时广播通知渠道。
type Channel struct {
	rt realtime.Publisher
}

// NewChannel 创建实时广播通知渠道实例。
func NewChannel(rt realtime.Publisher) *Channel {
	return &Channel{rt: rt}
}

// Name 返回渠道标识名。
func (c *Channel) Name() string {
	return "realtime"
}

// Format 将领域通知对象转换为实时广播载荷 *Message。
func (c *Channel) Format(n contract.Notification, notifiable any) (any, error) {
	if rtNotif, ok := n.(Notification); ok {
		msg := rtNotif.ToRealtime(notifiable)
		return &msg, nil
	}

	if cf, ok := n.(contract.ChannelFormatter); ok {
		raw := cf.Format(c.Name(), notifiable)
		if msg, ok := raw.(Message); ok {
			return &msg, nil
		}
		if msgPtr, ok := raw.(*Message); ok {
			return msgPtr, nil
		}
	}

	return nil, oops.Code(errs.CodeNotificationUnsupportedRT).
		With("notification", n).
		Errorf("notification does not implement realtime.Notification or ChannelFormatter")
}

// extractMessage 从任意输入中解析 *Message。
func (c *Channel) extractMessage(notifiable, message any) (*Message, error) {
	switch v := message.(type) {
	case *Message:
		return v, nil
	case Message:
		return &v, nil
	case contract.Notification:
		formatted, err := c.Format(v, notifiable)
		if err != nil {
			return nil, err
		}
		msg, ok := formatted.(*Message)
		if !ok {
			return nil, oops.Code(errs.CodeBadRequest).Errorf("realtime: formatted message is not *realtime.Message, got %T", formatted)
		}
		return msg, nil
	default:
		return nil, oops.Code(errs.CodeBadRequest).Errorf("realtime: expected *realtime.Message, got %T", message)
	}
}

// Send 执行实时广播通知发送。
func (c *Channel) Send(ctx context.Context, notifiable any, message any) error {
	if c.rt == nil {
		return nil
	}

	msg, err := c.extractMessage(notifiable, message)
	if err != nil {
		return err
	}

	return c.rt.DispatchContext(ctx, msg.Channel, msg.Event, msg.Data)
}
