// Package gotify 提供 Gotify 实时推送通知渠道与驱动实现。
package gotify

import (
	"context"
)

// Driver 定义 Gotify 底层传输驱动契约。
type Driver interface {
	// Name 返回驱动标识名（如 "http", "mock"）。
	Name() string

	// Send 执行向 Gotify 服务端推送消息。
	Send(ctx context.Context, msg *Message) error
}
