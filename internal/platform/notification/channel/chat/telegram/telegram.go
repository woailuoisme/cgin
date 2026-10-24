// Package telegram 提供基于 Telegram Bot API 的即时通讯通知驱动实现。
package telegram

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/samber/oops"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/notification/channel/chat"
)

const (
	defaultBaseURL = "https://api.telegram.org"
	defaultTimeout = 10 * time.Second
)

// Config Telegram 驱动配置。
type Config struct {
	BotToken string
	ChatID   string // 默认会话 ID
	BaseURL  string // 可自定义反向代理网关
	Timeout  time.Duration
}

// Driver Telegram 驱动。
type Driver struct {
	cfg    Config
	client *resty.Client
}

var _ chat.Driver = (*Driver)(nil)

// NewDriver 创建 Telegram 驱动实例。
func NewDriver(cfg Config) (*Driver, error) {
	if cfg.BotToken == "" {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("telegram: bot_token is required")
	}

	baseURL := cmp.Or(cfg.BaseURL, defaultBaseURL)
	timeout := cmp.Or(cfg.Timeout, defaultTimeout)

	client := resty.New().
		SetBaseURL(baseURL).
		SetTimeout(timeout)

	return &Driver{
		cfg:    cfg,
		client: client,
	}, nil
}

// Name 返回驱动标识名。
func (d *Driver) Name() string {
	return "telegram"
}

type telegramSendMessageRequest struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode,omitempty"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview,omitempty"`
	DisableNotification   bool   `json:"disable_notification,omitempty"`
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
	ErrorCode   int    `json:"error_code,omitempty"`
}

// Send 执行 Telegram 消息发送。
func (d *Driver) Send(ctx context.Context, msg *chat.Message) error {
	chatID := cmp.Or(msg.Recipient, d.cfg.ChatID)
	if chatID == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("telegram: chat_id is required")
	}

	if msg.Text == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("telegram: message text is required")
	}

	reqBody := telegramSendMessageRequest{
		ChatID:                chatID,
		Text:                  msg.Text,
		ParseMode:             msg.ParseMode,
		DisableWebPagePreview: msg.DisableWebPagePreview,
		DisableNotification:   msg.DisableNotification,
	}

	endpoint := fmt.Sprintf("/bot%s/sendMessage", d.cfg.BotToken)

	var res telegramResponse
	resp, err := d.client.R().
		SetContext(ctx).
		SetBody(reqBody).
		SetResult(&res).
		Post(endpoint)

	if err != nil {
		return oops.Code(errs.CodeInternal).In("TelegramDriver.Send").Wrap(err)
	}

	if resp.IsError() || !res.OK {
		return oops.Code(errs.CodeTelegramAPIError).
			With("status", resp.StatusCode()).
			With("error_code", res.ErrorCode).
			Errorf("telegram delivery failed: %s", res.Description)
	}

	return nil
}
