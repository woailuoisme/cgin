// Package mock 提供用于测试套件的内存短信驱动实现。
package mock

import (
	"context"
	"sync"

	"github.com/samber/lo"

	"bgin/internal/platform/notification/channel/sms"
)

// Driver 内存记录型短信驱动。
type Driver struct {
	mu       sync.RWMutex
	messages map[string][]*sms.Message
	otps     map[string][]string
}

var _ sms.Driver = (*Driver)(nil)

// NewDriver 创建内存 Mock 短信驱动实例。
func NewDriver() *Driver {
	return &Driver{
		messages: make(map[string][]*sms.Message),
		otps:     make(map[string][]string),
	}
}

// Name 返回驱动标识名。
func (d *Driver) Name() string {
	return "mock"
}

// Send 在内存中记录发往指定手机号的短信。
func (d *Driver) Send(_ context.Context, msg *sms.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	phone := msg.PhoneNumber
	d.messages[phone] = append(d.messages[phone], msg)

	// 提取 OTP 验证码（支持 TemplateParam["code"]、["otp"]、ParamSet 或直接 Content）
	code := msg.TemplateParam["code"]
	if code == "" {
		code = msg.TemplateParam["otp"]
	}
	if code == "" && len(msg.ParamSet) > 0 {
		code = msg.ParamSet[0]
	}
	if code == "" {
		code = msg.Content
	}

	if code != "" {
		d.otps[phone] = append(d.otps[phone], code)
	}

	return nil
}

// GetLastOTP 获取发往指定手机号的最新一次验证码。
func (d *Driver) GetLastOTP(phone string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return lo.LastOrEmpty(d.otps[phone])
}

// GetLastMessage 获取发往指定手机号的最新一条消息对象。
func (d *Driver) GetLastMessage(phone string) *sms.Message {
	d.mu.RLock()
	defer d.mu.RUnlock()
	list := d.messages[phone]
	if len(list) == 0 {
		return nil
	}
	return list[len(list)-1]
}

// GetMessages 获取发往指定手机号的全部消息列表。
func (d *Driver) GetMessages(phone string) []*sms.Message {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return append([]*sms.Message{}, d.messages[phone]...)
}

// Clear 清空所有记录的验证码与消息。
func (d *Driver) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.messages = make(map[string][]*sms.Message)
	d.otps = make(map[string][]string)
}
