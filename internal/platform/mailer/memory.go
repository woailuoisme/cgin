package mailer

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/samber/lo"

	"bgin/internal/platform/errs"
)

// MemoryMailer 纯内存邮件发送器，专用于脱机单元测试与发送行为断言。
type MemoryMailer struct {
	mu       sync.RWMutex
	messages []Message
	closed   bool
}

var _ Mailer = (*MemoryMailer)(nil)

// NewMemoryMailer 创建全新的内存邮件发送器。
func NewMemoryMailer() *MemoryMailer {
	return &MemoryMailer{
		messages: make([]Message, 0),
	}
}

// Send 记录单封邮件至内存消息列表。
func (m *MemoryMailer) Send(ctx context.Context, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return errors.New("mailer: memory mailer is closed")
	}

	if strings.TrimSpace(msg.To) == "" {
		return errs.BadRequest("email recipient 'To' cannot be empty")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	m.messages = append(m.messages, msg)
	return nil
}

// SendMailable 渲染结构化模板邮件并存入内存消息列表。
func (m *MemoryMailer) SendMailable(ctx context.Context, to string, mailable Mailable) error {
	return sendMailable(ctx, m, to, mailable)
}

// Close 标记邮件器为关闭状态。
func (m *MemoryMailer) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// Ping 检查内存发信器可用性。
func (m *MemoryMailer) Ping(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return errors.New("mailer: memory mailer is closed")
	}
	return ctx.Err()
}

// Driver 返回内存发信驱动标识。
func (m *MemoryMailer) Driver() string {
	return "memory"
}

// Enabled 报告内存发信驱动处于启用状态。
func (m *MemoryMailer) Enabled() bool {
	return true
}

// SentMessages 获取已发送的所有邮件副本。
func (m *MemoryMailer) SentMessages() []Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.messages)
}

// LastMessage 获取最后发送的一封邮件。
func (m *MemoryMailer) LastMessage() (Message, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return lo.Last(m.messages)
}

// Count 获取已发送邮件的总数。
func (m *MemoryMailer) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.messages)
}

// Reset 清空已发送邮件记录。
func (m *MemoryMailer) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = m.messages[:0]
}

// AssertSent 断言已发送邮件的数量是否符合预期。
func (m *MemoryMailer) AssertSent(tb testing.TB, expectedCount int) {
	tb.Helper()
	actual := m.Count()
	if actual != expectedCount {
		tb.Fatalf("mailer: expected %d sent messages, got %d", expectedCount, actual)
	}
}

// AssertSentTo 断言是否存在投递给目标收件人的邮件。
func (m *MemoryMailer) AssertSentTo(tb testing.TB, recipient string) {
	tb.Helper()
	msgs := m.SentMessages()
	found := slices.ContainsFunc(msgs, func(msg Message) bool {
		return msg.To == recipient || slices.Contains(msg.Cc, recipient) || slices.Contains(msg.Bcc, recipient)
	})
	if !found {
		tb.Fatalf("mailer: expected email sent to %q, but none was found", recipient)
	}
}
