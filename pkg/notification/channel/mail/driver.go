// Package mail 提供邮件通知渠道与驱动实现。
package mail

import (
	"context"
)

// Driver 定义邮件传输底层驱动契约。
type Driver interface {
	// Name 返回驱动标识名称（如 "resend", "smtp"）。
	Name() string

	// Send 发送已组装好的邮件消息。
	Send(ctx context.Context, msg *Message) error
}
