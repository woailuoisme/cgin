package notification

import (
	"context"

	"github.com/samber/oops"

	"cgin/pkg/errs"
	"cgin/pkg/realtime"
)

// RealtimeMessage 表示实时广播通知载荷。
type RealtimeMessage struct {
	Channel string `json:"channel"`
	Event   string `json:"event"`
	Data    any    `json:"data"`
}

// RealtimeNotification 声明支持实时广播的通知接口契约。
type RealtimeNotification interface {
	ToRealtime(notifiable any) RealtimeMessage
}

// RealtimeChannel 实时广播通知渠道。
type RealtimeChannel struct {
	rt realtime.Publisher
}

var (
	_ Channel   = (*RealtimeChannel)(nil)
	_ Formatter = (*RealtimeChannel)(nil)
)

// NewRealtimeChannel 创建实时广播通知渠道实例。
func NewRealtimeChannel(rt realtime.Publisher) *RealtimeChannel {
	return &RealtimeChannel{rt: rt}
}

// Name 返回渠道标识名。
func (c *RealtimeChannel) Name() string {
	return "realtime"
}

// Format 将领域通知对象转换为实时广播载荷 *RealtimeMessage。
func (c *RealtimeChannel) Format(n Notification, notifiable any) (any, error) {
	if rtNotif, ok := n.(RealtimeNotification); ok {
		msg := rtNotif.ToRealtime(notifiable)
		return &msg, nil
	}

	if cf, ok := n.(ChannelFormatter); ok {
		raw := cf.Format(c.Name(), notifiable)
		if msg, ok := raw.(RealtimeMessage); ok {
			return &msg, nil
		}
		if msgPtr, ok := raw.(*RealtimeMessage); ok {
			return msgPtr, nil
		}
	}

	return nil, oops.Code(errs.CodeNotificationUnsupportedRT).
		With("notification", n).
		Errorf("notification does not implement RealtimeNotification or ChannelFormatter")
}

func (c *RealtimeChannel) extractMessage(notifiable, message any) (*RealtimeMessage, error) {
	switch v := message.(type) {
	case *RealtimeMessage:
		return v, nil
	case RealtimeMessage:
		return &v, nil
	case Notification:
		formatted, err := c.Format(v, notifiable)
		if err != nil {
			return nil, err
		}
		msg, ok := formatted.(*RealtimeMessage)
		if !ok {
			return nil, oops.Code(errs.CodeBadRequest).Errorf("realtime: formatted message is not *RealtimeMessage, got %T", formatted)
		}
		return msg, nil
	default:
		return nil, oops.Code(errs.CodeBadRequest).Errorf("realtime: expected *RealtimeMessage, got %T", message)
	}
}

// Send 执行实时广播通知发送。
func (c *RealtimeChannel) Send(ctx context.Context, notifiable any, message any) error {
	if c.rt == nil {
		return nil
	}

	msg, err := c.extractMessage(notifiable, message)
	if err != nil {
		return err
	}

	return c.rt.DispatchContext(ctx, msg.Channel, msg.Event, msg.Data)
}
