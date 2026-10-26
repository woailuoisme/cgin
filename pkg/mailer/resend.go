package mailer

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/resend/resend-go/v4"
	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"cgin/internal/config"
	"cgin/pkg/errs"
	"cgin/pkg/logger"
	"cgin/pkg/resilience"
)

// resendDriver 基于 Resend 官方 SDK 实现的现代化邮件发送驱动。
type resendDriver struct {
	cfg    config.MailConfig
	logger *zerolog.Logger
	client *resend.Client
}

var _ Driver = (*resendDriver)(nil)

// newResendDriver 创建基于 Resend 官方 Go SDK 的邮件发送驱动实例。
func newResendDriver(cfg config.MailConfig, log *zerolog.Logger) (*resendDriver, error) {
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
func NewResendDriver(cfg config.MailConfig, log *zerolog.Logger) (Driver, error) {
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
