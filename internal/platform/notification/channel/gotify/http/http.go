// Package http 提供基于 Gotify 官方 REST API 的 HTTP 推送通知驱动实现。
package http

import (
	"cmp"
	"context"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/samber/oops"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/notification/channel/gotify"
)

const (
	defaultTimeout = 10 * time.Second
)

// Config Gotify HTTP 驱动配置。
type Config struct {
	ServerURL string
	AppToken  string
	Priority  int
	Timeout   time.Duration
}

// Driver Gotify 官方 REST API 驱动。
type Driver struct {
	cfg    Config
	client *resty.Client
}

var _ gotify.Driver = (*Driver)(nil)

// NewDriver 创建 Gotify HTTP 驱动实例。
func NewDriver(cfg Config) (*Driver, error) {
	timeout := cmp.Or(cfg.Timeout, defaultTimeout)
	client := resty.New().SetTimeout(timeout)

	return &Driver{
		cfg:    cfg,
		client: client,
	}, nil
}

// Name 返回驱动标识名。
func (d *Driver) Name() string {
	return "http"
}

type gotifyMessagePayload struct {
	Title    string         `json:"title,omitempty"`
	Message  string         `json:"message"`
	Priority int            `json:"priority"`
	Extras   map[string]any `json:"extras,omitempty"`
}

type gotifyErrorResponse struct {
	Error            string `json:"error"`
	ErrorCode        int    `json:"errorCode"`
	ErrorDescription string `json:"errorDescription"`
}

// Send 执行向 Gotify 发送推送通知。
func (d *Driver) Send(ctx context.Context, msg *gotify.Message) error {
	if msg == nil {
		return oops.Code(errs.CodeBadRequest).Errorf("gotify: message cannot be nil")
	}

	serverURL := cmp.Or(msg.ServerURL, d.cfg.ServerURL)
	if serverURL == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("gotify: server_url is required")
	}

	token := cmp.Or(msg.Token, d.cfg.AppToken)
	if token == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("gotify: app_token is required")
	}

	if msg.Message == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("gotify: message text is required")
	}

	priority := msg.Priority
	if priority == 0 && d.cfg.Priority > 0 {
		priority = d.cfg.Priority
	}

	reqBody := gotifyMessagePayload{
		Title:    msg.Title,
		Message:  msg.Message,
		Priority: priority,
		Extras:   msg.Extras,
	}

	endpoint := strings.TrimRight(serverURL, "/") + "/message"

	var errResp gotifyErrorResponse
	resp, err := d.client.R().
		SetContext(ctx).
		SetHeader("X-Gotify-Key", token).
		SetHeader("Content-Type", "application/json").
		SetBody(reqBody).
		SetError(&errResp).
		Post(endpoint)

	if err != nil {
		return oops.Code(errs.CodeInternal).In("GotifyHTTPDriver.Send").Wrap(err)
	}

	if resp.IsError() {
		errMsg := cmp.Or(errResp.ErrorDescription, errResp.Error, resp.Status())
		return oops.Code(errs.CodeGotifyAPIError).
			With("status", resp.StatusCode()).
			With("server_url", serverURL).
			Errorf("gotify notification delivery failed: %s (status: %d)", errMsg, resp.StatusCode())
	}

	return nil
}
