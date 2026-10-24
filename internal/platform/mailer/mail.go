// Package mailer 提供统一邮件发送抽象、模板渲染与多驱动支持（内置 Mailpit, Resend 与 Memory 驱动）。
package mailer

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/samber/oops"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
)

// Message 包含单封邮件的收发元数据、正文与附件。
type Message struct {
	// To 主收件人邮箱地址
	To string
	// Cc 抄送收件人邮箱列表
	Cc []string
	// Bcc 密送收件人邮箱列表
	Bcc []string
	// Subject 邮件主题
	Subject string
	// Body 邮件正文 (纯文本或 HTML)
	Body string
	// IsHTML 是否以 HTML 格式发送正文
	IsHTML bool
	// Attachments 邮件附件本地文件路径列表（向后兼容）
	Attachments []string
	// RawAttachments 结构化附件对象列表（支持内存二进制流与文件路径）
	RawAttachments []Attachment
}

// AllAttachments 归一化提取全部附件列表（合并 Attachments 路径与 RawAttachments 对象）。
func (m Message) AllAttachments() []Attachment {
	total := len(m.Attachments) + len(m.RawAttachments)
	if total == 0 {
		return nil
	}
	res := make([]Attachment, 0, total)
	for _, p := range m.Attachments {
		if strings.TrimSpace(p) != "" {
			res = append(res, NewAttachmentFromPath(p))
		}
	}
	res = append(res, m.RawAttachments...)
	return res
}

// Mailable 结构化邮件模板契约接口。
type Mailable interface {
	Subject() string
	Template() (string, map[string]any)
}

// Mailer 邮件发送契约接口，整合基础驱动与高层 Mailable 模板编排能力。
type Mailer interface {
	// Send 投递通用邮件消息（兼容纯文本与 HTML）
	Send(ctx context.Context, msg Message) error
	// SendMailable 渲染结构化模板邮件并投递
	SendMailable(ctx context.Context, to string, mailable Mailable) error
	// Close 释放驱动底层网络连接
	Close() error
	// Ping 探测发信服务可达性状态
	Ping(ctx context.Context) error
}

// DriverFactory 定义发信器驱动工厂函数。
type DriverFactory func(cfg config.MailConfig, log *zap.Logger) (Mailer, error)

var (
	driverMu      sync.RWMutex
	customDrivers = make(map[string]DriverFactory)
)

// RegisterDriver 注册自定义邮件发送器驱动工厂（遵循开闭原则 OCP，便于测试或第三方驱动接入）。
func RegisterDriver(name string, factory DriverFactory) {
	driverMu.Lock()
	defer driverMu.Unlock()
	customDrivers[strings.ToLower(strings.TrimSpace(name))] = factory
}

// New 根据 MailConfig 创建邮件发送器驱动实例（内置驱动静态分发，无 init 副作用）。
func New(cfg config.MailConfig, log *zap.Logger) (Mailer, error) {
	if log == nil {
		log = zap.NewNop()
	}
	if !cfg.IsEnabled() {
		log.Info("mail delivery is disabled (MAIL_ENABLED=false), falling back to log mailer")
		return NewLogMailer(log), nil
	}

	driverName := cmp.Or(strings.ToLower(strings.TrimSpace(cfg.Driver)), "mailpit")

	switch driverName {
	case "mailpit":
		return newMailpitMailer(cfg, log)
	case "resend":
		return newResendMailer(cfg, log)
	case "log":
		return NewLogMailer(log), nil
	case "null":
		return NewNullMailer(), nil
	case "memory":
		return NewMemoryMailer(), nil
	default:
		driverMu.RLock()
		factory, exists := customDrivers[driverName]
		driverMu.RUnlock()

		if !exists {
			return nil, oops.Code(string(errs.CodeInternal)).
				Errorf("unsupported mail driver: %s", cfg.Driver)
		}
		return factory(cfg, log)
	}
}

// sendMailable 将 Mailable 实体渲染为消息并通过目标驱动发送。
func sendMailable(ctx context.Context, sender Mailer, to string, mailable Mailable) error {
	if sender == nil {
		return oops.Code(string(errs.CodeInternal)).Errorf("mail driver cannot be nil")
	}
	if mailable == nil {
		return errs.BadRequest("mailable cannot be nil")
	}

	tmplName, data := mailable.Template()
	htmlBody, err := RenderTemplate(tmplName, data)
	if err != nil {
		return oops.Code(string(errs.CodeInternal)).Wrap(fmt.Errorf("mailer: render template %s failed: %w", tmplName, err))
	}

	return sender.Send(ctx, Message{
		To:      to,
		Subject: mailable.Subject(),
		Body:    htmlBody,
		IsHTML:  true,
	})
}

// maskRecipient 按最小可用原则掩码收件人地址：只保留域名，避免把个人数据写进日志与错误链。
func maskRecipient(to string) string {
	at := strings.LastIndex(to, "@")
	if at < 0 {
		return "***"
	}
	return "***" + to[at:]
}
