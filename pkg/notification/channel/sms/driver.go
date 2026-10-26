// Package sms 提供短消息通知渠道与多厂商驱动实现。
package sms

import (
	"context"
)

// Driver 定义短信传输底层驱动契约。
type Driver interface {
	// Name 返回驱动标识名称（如 "aliyun", "tencent", "smsbao", "log", "mock"）。
	Name() string

	// Send 发送短信消息。
	Send(ctx context.Context, msg *Message) error
}
