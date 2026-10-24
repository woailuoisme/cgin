package mailer

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/samber/lo"
	"github.com/samber/oops"
	gomail "github.com/wneessen/go-mail"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
)

// mailpitMailer 基于 wneessen/go-mail 实现的 Mailpit 本地测试发信器。
type mailpitMailer struct {
	cfg    config.MailConfig
	logger *zap.Logger
	client *gomail.Client
}

var _ Mailer = (*mailpitMailer)(nil)

// newMailpitMailer 创建 Mailpit 发信器实例。
func newMailpitMailer(cfg config.MailConfig, log *zap.Logger) (*mailpitMailer, error) {
	log = cmp.Or(log, zap.NewNop())
	host := cmp.Or(strings.TrimSpace(cfg.Mailpit.Host), "127.0.0.1")
	port := cmp.Or(cfg.Mailpit.Port, 1025)

	opts := []gomail.Option{
		gomail.WithPort(port),
		gomail.WithTimeout(10 * time.Second),
		gomail.WithSMTPAuth(gomail.SMTPAuthNoAuth),
		gomail.WithTLSPolicy(gomail.NoTLS),
	}

	client, err := gomail.NewClient(host, opts...)
	if err != nil {
		return nil, oops.Code(string(errs.CodeInternal)).
			With("host", host).
			With("port", port).
			Wrap(fmt.Errorf("mailpit: create client failed: %w", err))
	}

	return &mailpitMailer{
		cfg:    cfg,
		logger: log,
		client: client,
	}, nil
}

// Send 发送单封邮件消息。
func (m *mailpitMailer) Send(ctx context.Context, msg Message) error {
	if strings.TrimSpace(msg.To) == "" {
		return errs.BadRequest("email recipient 'To' cannot be empty")
	}

	if m.client == nil {
		return errs.Internal(errors.New("mailpit client is not initialized"))
	}

	emailMsg, err := m.buildMessage(msg)
	if err != nil {
		return err
	}

	if err := m.client.DialAndSendWithContext(ctx, emailMsg); err != nil {
		return oops.Code(string(errs.CodeInternal)).
			With("to", maskRecipient(msg.To)).
			With("subject", msg.Subject).
			Wrap(fmt.Errorf("mailpit: send email failed: %w", err))
	}

	m.logger.Debug("mailpit email delivered successfully",
		zap.String("to", maskRecipient(msg.To)),
		zap.String("subject", msg.Subject),
	)
	return nil
}

// SendMailable 将 Mailable 实体渲染为邮件消息并发送。
func (m *mailpitMailer) SendMailable(ctx context.Context, to string, mailable Mailable) error {
	return sendMailable(ctx, m, to, mailable)
}

// Close 释放底层的 SMTP 连接池资源。
func (m *mailpitMailer) Close() error {
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}

// Ping 检查与 Mailpit SMTP 服务的网络连通性。
func (m *mailpitMailer) Ping(ctx context.Context) error {
	if m.client == nil {
		return errors.New("mailpit client is not initialized")
	}
	host := cmp.Or(strings.TrimSpace(m.cfg.Mailpit.Host), "127.0.0.1")
	port := cmp.Or(m.cfg.Mailpit.Port, 1025)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mailpit: tcp dial %s failed: %w", addr, err)
	}
	_ = conn.Close()
	return nil
}

// Driver 返回底层驱动标识。
func (m *mailpitMailer) Driver() string {
	return "mailpit"
}

// Enabled 报告 Mailpit 驱动是否启用。
func (m *mailpitMailer) Enabled() bool {
	return m.cfg.IsEnabled()
}

// buildMessage 将 Message 结构转换为 gomail.Msg 实例。
func (m *mailpitMailer) buildMessage(msg Message) (*gomail.Msg, error) {
	emailMsg := gomail.NewMsg()

	if err := emailMsg.FromFormat(m.cfg.FromName, m.cfg.FromAddress); err != nil {
		return nil, oops.Code(string(errs.CodeInternal)).Wrap(fmt.Errorf("mailpit: set from address failed: %w", err))
	}

	if err := emailMsg.To(msg.To); err != nil {
		return nil, oops.Code(string(errs.CodeBadRequest)).Wrap(fmt.Errorf("mailpit: set to address failed: %w", err))
	}

	if len(msg.Cc) > 0 {
		if err := emailMsg.Cc(msg.Cc...); err != nil {
			return nil, oops.Code(string(errs.CodeBadRequest)).Wrap(fmt.Errorf("mailpit: set cc address failed: %w", err))
		}
	}

	if len(msg.Bcc) > 0 {
		if err := emailMsg.Bcc(msg.Bcc...); err != nil {
			return nil, oops.Code(string(errs.CodeBadRequest)).Wrap(fmt.Errorf("mailpit: set bcc address failed: %w", err))
		}
	}

	emailMsg.Subject(msg.Subject)
	bodyType := lo.Ternary(msg.IsHTML, gomail.TypeTextHTML, gomail.TypeTextPlain)
	emailMsg.SetBodyString(bodyType, msg.Body)

	for _, att := range msg.AllAttachments() {
		if len(att.Content) > 0 {
			if err := emailMsg.AttachReader(att.Filename, bytes.NewReader(att.Content)); err != nil {
				return nil, oops.Code(string(errs.CodeInternal)).Wrap(fmt.Errorf("mailpit: attach reader failed: %w", err))
			}
		} else if strings.TrimSpace(att.Path) != "" {
			emailMsg.AttachFile(att.Path)
		}
	}

	return emailMsg, nil
}
