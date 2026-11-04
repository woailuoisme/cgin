// Package mailer 提供统一邮件发送抽象、模板渲染与 Mailpit / Resend 驱动支持。
package mailer

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"cgin/internal/config"
	"cgin/pkg/errs"
	"cgin/pkg/logger"
)

// Attachment 表示邮件附件，既支持纯内存字节流（如 PDF/Excel 生成），也兼容本地文件路径。
type Attachment struct {
	// Filename 附件名称（如 invoice.pdf）
	Filename string
	// Content 内存中的二进制内容（若 Content 非空，则优先使用 Content，无需磁盘临时文件）
	Content []byte
	// Path 本地文件系统路径（可选，兼容传统文件附件）
	Path string
	// ContentType MIME 媒体类型（如 application/pdf，可选）
	ContentType string
}

// NewAttachmentFromBytes 创建基于内存二进制字节流的附件对象（零磁盘 I/O）。
// 采用防御性拷贝隔离输入切片，遵循 Uber Go Style Guide 边界规范。
func NewAttachmentFromBytes(filename string, content []byte, contentType ...string) Attachment {
	var ct string
	if len(contentType) > 0 {
		ct = contentType[0]
	}
	return Attachment{
		Filename:    filename,
		Content:     slices.Clone(content),
		ContentType: ct,
	}
}

// NewAttachmentFromPath 创建基于本地文件系统路径的附件对象。
func NewAttachmentFromPath(path string, filename ...string) Attachment {
	name := filepath.Base(path)
	if len(filename) > 0 && filename[0] != "" {
		name = filename[0]
	}
	return Attachment{
		Filename: name,
		Path:     path,
	}
}

// Bytes 获取附件的二进制字节数据；若由 Path 构造则动态从文件系统读取。
// 返回 Content 的防御性副本，防止外部调用方篡改底层字节数组。
func (a Attachment) Bytes() ([]byte, error) {
	if len(a.Content) > 0 {
		return slices.Clone(a.Content), nil
	}
	if a.Path != "" {
		return os.ReadFile(a.Path)
	}
	return nil, errors.New("mailer: attachment has neither content nor path")
}

// Message 包含单封或批量邮件的收发元数据、正文与结构化附件。
type Message struct {
	// To 主收件人邮箱列表（支持单收件人与多收件人）
	To []string
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
	// Attachments 统一结构化附件列表
	Attachments []Attachment
}

// NewMessage 创建全新的空 Message 实体，支持链式 Fluent 构造。
func NewMessage() Message {
	return Message{}
}

// WithTo 链式追加主收件人。
func (m Message) WithTo(to ...string) Message {
	for _, t := range to {
		if trimmed := strings.TrimSpace(t); trimmed != "" {
			m.To = append(m.To, trimmed)
		}
	}
	return m
}

// WithCc 链式追加抄送收件人。
func (m Message) WithCc(cc ...string) Message {
	for _, c := range cc {
		if trimmed := strings.TrimSpace(c); trimmed != "" {
			m.Cc = append(m.Cc, trimmed)
		}
	}
	return m
}

// WithBcc 链式追加密送收件人。
func (m Message) WithBcc(bcc ...string) Message {
	for _, b := range bcc {
		if trimmed := strings.TrimSpace(b); trimmed != "" {
			m.Bcc = append(m.Bcc, trimmed)
		}
	}
	return m
}

// WithSubject 链式设置邮件主题。
func (m Message) WithSubject(subject string) Message {
	m.Subject = subject
	return m
}

// WithHTML 链式设置 HTML 格式邮件正文。
func (m Message) WithHTML(html string) Message {
	m.Body = html
	m.IsHTML = true
	return m
}

// WithText 链式设置纯文本格式邮件正文。
func (m Message) WithText(text string) Message {
	m.Body = text
	m.IsHTML = false
	return m
}

// WithAttachment 链式追加结构化附件。
func (m Message) WithAttachment(att ...Attachment) Message {
	m.Attachments = append(m.Attachments, att...)
	return m
}

// WithAttachmentFile 链式追加本地文件附件。
func (m Message) WithAttachmentFile(path string, filename ...string) Message {
	m.Attachments = append(m.Attachments, NewAttachmentFromPath(path, filename...))
	return m
}

// WithAttachmentBytes 链式追加基于内存二进制流的附件。
func (m Message) WithAttachmentBytes(filename string, content []byte, contentType ...string) Message {
	m.Attachments = append(m.Attachments, NewAttachmentFromBytes(filename, content, contentType...))
	return m
}

// AllAttachments 返回全部结构化附件的副本（向后兼容）。
func (m Message) AllAttachments() []Attachment {
	if len(m.Attachments) == 0 {
		return nil
	}
	return slices.Clone(m.Attachments)
}

// PrimaryRecipient 获取首个主收件人邮箱（收件人为空时返回空串）。
func (m Message) PrimaryRecipient() string {
	if len(m.To) == 0 {
		return ""
	}
	return m.To[0]
}

// Mailable 结构化邮件模板契约接口。
type Mailable interface {
	Subject() string
	Template() (string, map[string]any)
}

// Driver 定义底层邮件传输协议驱动契约，由具体传输实现（Mailpit SMTP, Resend HTTP, Memory 等）承载。
type Driver interface {
	// Send 将邮件消息通过协议传输通道投递
	Send(ctx context.Context, msg Message) error
	// Ping 探针探测发信通道可达性
	Ping(ctx context.Context) error
	// Close 释放底层连接与资源
	Close() error
}

// Mailer 邮件发送契约接口，整合基础驱动、中间件与高层 Mailable 模板编排能力，原生满足优雅停机契约。
type Mailer interface {
	// Send 投递通用邮件消息（兼容纯文本与 HTML）
	Send(ctx context.Context, msg Message) error
	// SendMailable 渲染结构化模板邮件并投递
	SendMailable(ctx context.Context, to string, mailable Mailable) error
	// Close 释放驱动底层网络连接
	Close() error
	// Shutdown 满足 do.ShutdownerWithError 优雅停机契约
	Shutdown() error
	// Ping 探测发信服务可达性状态
	Ping(ctx context.Context) error
}

// Manager 邮件管理器，实现 Mailer 契约与 do.ShutdownerWithError 优雅停机契约。
type Manager struct {
	driver Driver
	log    *zerolog.Logger
}

var _ Mailer = (*Manager)(nil)

// NewManager 封装底层 Driver 构建 Manager 实例。
func NewManager(driver Driver, log ...*zerolog.Logger) *Manager {
	var l *zerolog.Logger
	if len(log) > 0 && log[0] != nil {
		l = log[0]
	} else {
		l = logger.Nop()
	}
	return &Manager{
		driver: driver,
		log:    l,
	}
}

// Driver 返回底层驱动。
func (m *Manager) Driver() Driver {
	return m.driver
}

// Send 统一收敛校验与日志投递邮件。
func (m *Manager) Send(ctx context.Context, msg Message) error {
	if len(msg.To) == 0 {
		return errs.BadRequest("email recipient 'To' cannot be empty")
	}
	if m.driver == nil {
		return oops.Code(errs.CodeInternal).Errorf("mailer: driver is not initialized")
	}

	if err := m.driver.Send(ctx, msg); err != nil {
		m.log.Error().
			Err(err).
			Strs("to", maskRecipients(msg.To)).
			Strs("cc", maskRecipients(msg.Cc)).
			Strs("bcc", maskRecipients(msg.Bcc)).
			Str("subject", msg.Subject).
			Int("attachments", len(msg.Attachments)).
			Msg("mail delivery failed")
		return err
	}

	m.log.Debug().
		Strs("to", maskRecipients(msg.To)).
		Strs("cc", maskRecipients(msg.Cc)).
		Strs("bcc", maskRecipients(msg.Bcc)).
		Str("subject", msg.Subject).
		Int("attachments", len(msg.Attachments)).
		Bool("is_html", msg.IsHTML).
		Msg("mail delivered successfully")
	return nil
}

// SendMailable 渲染结构化模板邮件并投递。
func (m *Manager) SendMailable(ctx context.Context, to string, mailable Mailable) error {
	to = strings.TrimSpace(to)
	if to == "" {
		return errs.BadRequest("email recipient 'To' cannot be empty")
	}
	if mailable == nil {
		return errs.BadRequest("mailable cannot be nil")
	}

	tmplName, data := mailable.Template()
	htmlBody, err := RenderTemplate(tmplName, data)
	if err != nil {
		return oops.Code(errs.CodeInternal).Wrapf(err, "mailer: render template %s", tmplName)
	}

	return m.Send(ctx, Message{
		To:      []string{to},
		Subject: mailable.Subject(),
		Body:    htmlBody,
		IsHTML:  true,
	})
}

// Ping 检查驱动可用性。
func (m *Manager) Ping(ctx context.Context) error {
	if m.driver == nil {
		return errors.New("mailer: driver is not initialized")
	}
	return m.driver.Ping(ctx)
}

// Close 释放驱动底层资源。
func (m *Manager) Close() error {
	if m.driver != nil {
		return m.driver.Close()
	}
	return nil
}

// Shutdown 原生满足 do.ShutdownerWithError 优雅停机契约。
func (m *Manager) Shutdown() error {
	return m.Close()
}

// New 根据 MailConfig 创建邮件管理器实例（支持 mailpit 与 resend 驱动，默认 mailpit）。
func New(cfg config.MailConfig, log *zerolog.Logger) (Mailer, error) {
	log = cmp.Or(log, logger.Nop())

	driverName := cmp.Or(strings.ToLower(strings.TrimSpace(cfg.Driver)), "mailpit")

	var (
		driver Driver
		err    error
	)

	switch driverName {
	case "mailpit":
		driver, err = newMailpitDriver(cfg, log)
	case "resend":
		driver, err = newResendDriver(cfg, log)
	default:
		return nil, oops.Code(errs.CodeInternal).
			Errorf("unsupported mail driver: %s", cfg.Driver)
	}

	if err != nil {
		return nil, err
	}

	return NewManager(driver, log), nil
}

// maskRecipient 按最小可用原则掩码收件人地址：只保留域名，避免把个人数据写进日志与错误链。
func maskRecipient(to string) string {
	if _, domain, found := strings.CutLast(to, "@"); found {
		return "***@" + domain
	}
	return "***"
}

// maskRecipients 批量掩码收件人切片。
func maskRecipients(recipients []string) []string {
	if len(recipients) == 0 {
		return nil
	}
	res := make([]string, len(recipients))
	for i, r := range recipients {
		res[i] = maskRecipient(r)
	}
	return res
}
