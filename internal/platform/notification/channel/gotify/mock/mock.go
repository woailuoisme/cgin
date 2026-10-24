// Package mock 提供用于测试套件的 Gotify 内存通知驱动实现。
package mock

import (
	"context"
	"sync"

	"bgin/internal/platform/notification/channel/gotify"
)

// Driver 内存记录型 Gotify 驱动。
type Driver struct {
	mu       sync.RWMutex
	messages []*gotify.Message
}

var _ gotify.Driver = (*Driver)(nil)

// NewDriver 创建内存 Mock Gotify 驱动实例。
func NewDriver() *Driver {
	return &Driver{
		messages: make([]*gotify.Message, 0),
	}
}

// Name 返回驱动标识名。
func (d *Driver) Name() string {
	return "mock"
}

// Send 在内存中记录推送的 Gotify 消息。
func (d *Driver) Send(_ context.Context, msg *gotify.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.messages = append(d.messages, msg)
	return nil
}

// GetLastMessage 获取最新一条收到的 Gotify 消息。
func (d *Driver) GetLastMessage() *gotify.Message {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.messages) == 0 {
		return nil
	}
	return d.messages[len(d.messages)-1]
}

// GetMessages 获取全部收到的 Gotify 消息列表副本。
func (d *Driver) GetMessages() []*gotify.Message {
	d.mu.RLock()
	defer d.mu.RUnlock()
	res := make([]*gotify.Message, len(d.messages))
	copy(res, d.messages)
	return res
}

// Clear 清空所有已记录的消息。
func (d *Driver) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.messages = make([]*gotify.Message, 0)
}
