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
	"github.com/samber/oops"
	"go.uber.org/zap"

	"cgin/internal/config"
	"cgin/internal/platform/errs"
	"cgin/internal/platform/resilience"
)

// resendMailer 基于 Resend 官方 SDK 实现的现代化邮件发送器。
type resendMailer struct {
	cfg    config.MailConfig
	logger *zap.Logger
	client *resend.Client
}

var _ Mailer = (*resendMailer)(nil)

// newResendMailer 创建基于 Resend 官方 Go SDK 的邮件发送器实例。
func newResendMailer(cfg config.MailConfig, log *zap.Logger) (*resendMailer, error) {
	log = cmp.Or(log, zap.NewNop())
	apiKey := strings.TrimSpace(cfg.Resend.APIKey)
	if apiKey == "" {
		return nil, oops.Code(string(errs.CodeInternal)).Wrap(errors.New("resend: api key cannot be empty"))
	}

	httpClient := resilience.NewResilientHTTPClient(resilience.ResilientTransportConfig{
		Timeout: 15 * time.Second,
	})

	client := resend.NewCustomClient(httpClient, apiKey)
	client.UserAgent = "cgin-mailer/1.0"

	baseURLStr := strings.TrimRight(cmp.Or(strings.TrimSpace(cfg.Resend.BaseURL), "https://api.resend.com"), "/") + "/"
	parsedURL, err := url.Parse(baseURLStr)
	if err != nil {
		return nil, oops.Code(string(errs.CodeInternal)).Wrap(fmt.Errorf("resend: invalid base url %q: %w", cfg.Resend.BaseURL, err))
	}
	client.BaseURL = parsedURL

	return &resendMailer{
		cfg:    cfg,
		logger: log,
		client: client,
	}, nil
}

// Send 发送单封邮件。
func (m *resendMailer) Send(ctx context.Context, msg Message) error {
	if strings.TrimSpace(msg.To) == "" {
		return errs.BadRequest("email recipient 'To' cannot be empty")
	}

	reqPayload, err := m.buildRequest(msg)
	if err != nil {
		return err
	}

	resp, err := m.client.Emails.SendWithContext(ctx, reqPayload)
	if err != nil {
		if rateLimitErr, ok := errors.AsType[*resend.RateLimitError](err); ok {
			return oops.Code(string(errs.CodeRateLimited)).
				With("to", maskRecipient(msg.To)).
				With("subject", msg.Subject).
				Wrap(fmt.Errorf("resend: rate limit exceeded: %w", rateLimitErr))
		}

		return oops.Code(string(errs.CodeServiceUnavailable)).
			With("to", maskRecipient(msg.To)).
			With("subject", msg.Subject).
			Wrap(fmt.Errorf("resend: send email failed: %w", err))
	}

	m.logger.Debug("resend email sent successfully",
		zap.String("email_id", resp.Id),
		zap.String("to", maskRecipient(msg.To)),
		zap.String("subject", msg.Subject),
	)

	return nil
}

// SendMailable 渲染模板并发送结构化邮件。
func (m *resendMailer) SendMailable(ctx context.Context, to string, mailable Mailable) error {
	return sendMailable(ctx, m, to, mailable)
}

// Close 释放资源（HTTP 客户端无持久连接池，直接返回 nil）。
func (m *resendMailer) Close() error {
	return nil
}

// Ping 验证 Resend 客户端配置与上下文有效性。
// 此处做本地凭据非空校验与上下文超时检查，避免高频就绪探测产生外部计费 API 调用或触发三方频控限制。
func (m *resendMailer) Ping(ctx context.Context) error {
	if m.client == nil {
		return errors.New("resend: client is not initialized")
	}
	if strings.TrimSpace(m.cfg.Resend.APIKey) == "" {
		return errors.New("resend: api key cannot be empty")
	}
	return ctx.Err()
}

// Driver 返回底层驱动标识。
func (m *resendMailer) Driver() string {
	return "resend"
}

// Enabled 报告 Resend 驱动是否启用。
func (m *resendMailer) Enabled() bool {
	return m.cfg.IsEnabled()
}

// buildRequest 组装 Resend 官方 SDK 发送邮件请求体。
func (m *resendMailer) buildRequest(msg Message) (*resend.SendEmailRequest, error) {
	from := cmp.Or(strings.TrimSpace(m.cfg.FromAddress), "onboarding@resend.dev")
	if m.cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", m.cfg.FromName, from)
	}

	req := &resend.SendEmailRequest{
		From:    from,
		To:      []string{msg.To},
		Subject: msg.Subject,
		Cc:      msg.Cc,
		Bcc:     msg.Bcc,
	}

	if msg.IsHTML {
		req.Html = msg.Body
	} else {
		req.Text = msg.Body
	}

	allAtts := msg.AllAttachments()
	if len(allAtts) > 0 {
		attachments := make([]*resend.Attachment, 0, len(allAtts))
		for _, att := range allAtts {
			data, err := att.Bytes()
			if err != nil {
				return nil, oops.Code(string(errs.CodeBadRequest)).
					Wrap(fmt.Errorf("resend: read attachment %s failed: %w", att.Filename, err))
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
