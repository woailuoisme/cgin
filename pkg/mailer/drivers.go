package mailer

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/resend/resend-go/v4"
	"github.com/samber/oops"
	gomail "github.com/wneessen/go-mail"

	"cgin/internal/config"
	"cgin/pkg/errs"
	"cgin/pkg/logger"
	"cgin/pkg/resilience"
)

// mailpitDriver 基于 wneessen/go-mail 实现的 Mailpit 本地测试发信驱动。
type mailpitDriver struct {
	cfg    config.MailConfig
	logger *slog.Logger
	client *gomail.Client
}

var _ Driver = (*mailpitDriver)(nil)

// newMailpitDriver 创建 Mailpit 发信驱动实例。
func newMailpitDriver(cfg config.MailConfig, log *slog.Logger) (*mailpitDriver, error) {
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
func NewMailpitDriver(cfg config.MailConfig, log *slog.Logger) (Driver, error) {
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

// resendDriver 基于 Resend 官方 SDK 实现的现代化邮件发送驱动。
type resendDriver struct {
	cfg    config.MailConfig
	logger *slog.Logger
	client *resend.Client
}

var _ Driver = (*resendDriver)(nil)

// newResendDriver 创建基于 Resend 官方 Go SDK 的邮件发送驱动实例。
func newResendDriver(cfg config.MailConfig, log *slog.Logger) (*resendDriver, error) {
	log = cmp.Or(log, logger.Nop())
	apiKey := strings.TrimSpace(cfg.Resend.APIKey)
	if apiKey == "" {
		return nil, oops.Code(errs.CodeInternal).Wrap(errors.New("resend: api key cannot be empty"))
	}

	httpClient := resilience.NewResilientHTTPClient(resilience.ResilientTransportConfig{
		Timeout: 15 * time.Second,
	})

	client := resend.NewCustomClient(httpClient, apiKey)
	client.UserAgent = "cgin-mailer/1.0"

	baseURLStr := strings.TrimRight(cmp.Or(strings.TrimSpace(cfg.Resend.BaseURL), "https://api.resend.com"), "/") + "/"
	parsedURL, err := url.Parse(baseURLStr)
	if err != nil {
		return nil, oops.Code(errs.CodeInternal).Wrapf(err, "resend: invalid base url %q", cfg.Resend.BaseURL)
	}
	client.BaseURL = parsedURL

	return &resendDriver{
		cfg:    cfg,
		logger: log,
		client: client,
	}, nil
}

// NewResendDriver 是 newResendDriver 的对外导出版本。
func NewResendDriver(cfg config.MailConfig, log *slog.Logger) (Driver, error) {
	return newResendDriver(cfg, log)
}

// Send 发送单封或批量邮件。
func (m *resendDriver) Send(ctx context.Context, msg Message) error {
	if len(msg.To) == 0 {
		return errs.BadRequest("email recipient 'To' cannot be empty")
	}

	reqPayload, err := m.buildRequest(msg)
	if err != nil {
		return err
	}

	_, err = m.client.Emails.SendWithContext(ctx, reqPayload)
	if err != nil {
		if rateLimitErr, ok := errors.AsType[*resend.RateLimitError](err); ok {
			return oops.Code(errs.CodeRateLimited).
				With("to", maskRecipients(msg.To)).
				With("subject", msg.Subject).
				Wrapf(rateLimitErr, "resend: rate limit exceeded")
		}

		return oops.Code(errs.CodeServiceUnavailable).
			With("to", maskRecipients(msg.To)).
			With("subject", msg.Subject).
			Wrapf(err, "resend: send email failed")
	}

	return nil
}

// Close 释放资源（HTTP 客户端无持久连接池，直接返回 nil）。
func (m *resendDriver) Close() error {
	return nil
}

// Ping 验证 Resend 客户端配置与上下文有效性。
// 此处做本地凭据非空校验与上下文超时检查，避免高频就绪探测产生外部计费 API 调用或触发三方频控限制。
func (m *resendDriver) Ping(ctx context.Context) error {
	if m.client == nil {
		return errors.New("resend: client is not initialized")
	}
	if strings.TrimSpace(m.cfg.Resend.APIKey) == "" {
		return errors.New("resend: api key cannot be empty")
	}
	return ctx.Err()
}

// Driver 返回底层驱动标识。
func (m *resendDriver) Driver() string {
	return "resend"
}

// Enabled 报告 Resend 驱动是否启用。
func (m *resendDriver) Enabled() bool {
	return m.cfg.IsEnabled()
}

// buildRequest 组装 Resend 官方 SDK 发送邮件请求体。
func (m *resendDriver) buildRequest(msg Message) (*resend.SendEmailRequest, error) {
	from := cmp.Or(strings.TrimSpace(m.cfg.FromAddress), "onboarding@resend.dev")
	if m.cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", m.cfg.FromName, from)
	}

	req := &resend.SendEmailRequest{
		From:    from,
		To:      msg.To,
		Subject: msg.Subject,
		Cc:      msg.Cc,
		Bcc:     msg.Bcc,
	}

	if msg.IsHTML {
		req.Html = msg.Body
	} else {
		req.Text = msg.Body
	}

	if len(msg.Attachments) > 0 {
		attachments := make([]*resend.Attachment, 0, len(msg.Attachments))
		for _, att := range msg.Attachments {
			data, err := att.Bytes()
			if err != nil {
				return nil, oops.Code(errs.CodeBadRequest).
					Wrapf(err, "resend: read attachment %s failed", att.Filename)
			}
			attachments = append(attachments, &resend.Attachment{
				Filename:    att.Filename,
				Content:     data,
				ContentType: att.ContentType,
			})
		}
		req.Attachments = attachments
	}

	return req, nil
}
