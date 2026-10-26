// Package chat 提供即时通讯（如 Telegram、飞书等）渠道与驱动实现。
package chat

import (
	"context"
)

// Driver 定义即时通讯传输底层驱动契约。
type Driver interface {
	// Name 返回驱动标识名称（如 "telegram"）。
	Name() string

	// Send 发送即时通讯消息。
	Send(ctx context.Context, msg *Message) error
}
