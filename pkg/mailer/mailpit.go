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

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gomail "github.com/wneessen/go-mail"

	"cgin/internal/config"
	"cgin/pkg/errs"
	"cgin/pkg/logger"
)

// mailpitDriver 基于 wneessen/go-mail 实现的 Mailpit 本地测试发信驱动。
type mailpitDriver struct {
	cfg    config.MailConfig
	logger *zerolog.Logger
	client *gomail.Client
}

var _ Driver = (*mailpitDriver)(nil)

// newMailpitDriver 创建 Mailpit 发信驱动实例。
func newMailpitDriver(cfg config.MailConfig, log *zerolog.Logger) (*mailpitDriver, error) {
	log = cmp.Or(log, logger.Nop())
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
		return nil, oops.Code(errs.CodeInternal).
			With("host", host).
			With("port", port).
			Wrapf(err, "mailpit: create client failed")
	}

	return &mailpitDriver{
		cfg:    cfg,
		logger: log,
		client: client,
	}, nil
}

// NewMailpitDriver 是 newMailpitDriver 的对外导出版本。
func NewMailpitDriver(cfg config.MailConfig, log *zerolog.Logger) (Driver, error) {
	return newMailpitDriver(cfg, log)
}

// Send 发送邮件消息。
func (m *mailpitDriver) Send(ctx context.Context, msg Message) error {
	if len(msg.To) == 0 {
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
		return oops.Code(errs.CodeInternal).
			With("to", maskRecipients(msg.To)).
			With("subject", msg.Subject).
			Wrapf(err, "mailpit: send email failed")
	}

	return nil
}

// Close 释放底层的 SMTP 连接池资源。
func (m *mailpitDriver) Close() error {
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}

// Ping 检查与 Mailpit SMTP 服务的网络连通性。
func (m *mailpitDriver) Ping(ctx context.Context) error {
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
	defer func() { _ = conn.Close() }()
	return nil
}

// Driver 返回底层驱动标识。
func (m *mailpitDriver) Driver() string {
	return "mailpit"
}

// Enabled 报告 Mailpit 驱动是否启用。
func (m *mailpitDriver) Enabled() bool {
	return m.cfg.IsEnabled()
}

// buildMessage 将 Message 结构转换为 gomail.Msg 实例。
func (m *mailpitDriver) buildMessage(msg Message) (*gomail.Msg, error) {
	emailMsg := gomail.NewMsg()

	if err := emailMsg.FromFormat(m.cfg.FromName, m.cfg.FromAddress); err != nil {
		return nil, oops.Code(errs.CodeInternal).Wrapf(err, "mailpit: set from address failed")
	}

	if err := emailMsg.To(msg.To...); err != nil {
		return nil, oops.Code(errs.CodeBadRequest).Wrapf(err, "mailpit: set to address failed")
	}

	if len(msg.Cc) > 0 {
		if err := emailMsg.Cc(msg.Cc...); err != nil {
			return nil, oops.Code(errs.CodeBadRequest).Wrapf(err, "mailpit: set cc address failed")
		}
	}

	if len(msg.Bcc) > 0 {
		if err := emailMsg.Bcc(msg.Bcc...); err != nil {
			return nil, oops.Code(errs.CodeBadRequest).Wrapf(err, "mailpit: set bcc address failed")
		}
	}

	emailMsg.Subject(msg.Subject)
	bodyType := gomail.TypeTextPlain
	if msg.IsHTML {
		bodyType = gomail.TypeTextHTML
	}
	emailMsg.SetBodyString(bodyType, msg.Body)

	for _, att := range msg.Attachments {
		if len(att.Content) > 0 {
			if err := emailMsg.AttachReader(att.Filename, bytes.NewReader(att.Content)); err != nil {
				return nil, oops.Code(errs.CodeInternal).Wrapf(err, "mailpit: attach reader failed")
			}
		} else if strings.TrimSpace(att.Path) != "" {
			emailMsg.AttachFile(att.Path)
		}
	}

	return emailMsg, nil
}
