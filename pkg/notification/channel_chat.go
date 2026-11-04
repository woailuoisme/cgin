package notification

import (
	"cmp"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/samber/lo"
	"github.com/samber/oops"

	"cgin/pkg/errs"
)

const (
	defaultTelegramBaseURL = "https://api.telegram.org"
	defaultTelegramTimeout = 10 * time.Second
)

// ChatMessage 表示标准的即时通讯通知载荷。
type ChatMessage struct {
	Recipient             string
	Text                  string
	ParseMode             string // "Markdown", "MarkdownV2", "HTML"
	DisableWebPagePreview bool
	DisableNotification   bool
}

// NewChatMessage 创建初始化为空的即时通讯消息对象。
func NewChatMessage() *ChatMessage {
	return &ChatMessage{}
}

// To 设置接收方标识（如 Telegram ChatID）。
func (m *ChatMessage) To(recipient string) *ChatMessage {
	m.Recipient = recipient
	return m
}

// WithText 设置消息正文文本。
func (m *ChatMessage) WithText(text string) *ChatMessage {
	m.Text = text
	return m
}

// WithParseMode 设置格式化解析模式。
func (m *ChatMessage) WithParseMode(mode string) *ChatMessage {
	m.ParseMode = mode
	return m
}

// Silent 设置静默投递。
func (m *ChatMessage) Silent() *ChatMessage {
	m.DisableNotification = true
	return m
}

// ChatNotification 声明支持即时通讯格式化的通知接口契约。
type ChatNotification interface {
	ToChat(notifiable any) any
}

// ChatDriver 定义底层即时通讯平台（如 Telegram、飞书、钉钉）的发送驱动契约。
type ChatDriver interface {
	Name() string
	Send(ctx context.Context, msg *ChatMessage) error
}

// ChatChannel 即时通讯通知渠道实现。
type ChatChannel struct {
	mu            sync.RWMutex
	drivers       map[string]ChatDriver
	defaultDriver string
}

var (
	_ Channel   = (*ChatChannel)(nil)
	_ Formatter = (*ChatChannel)(nil)
)

// NewChatChannel 创建即时通讯通知渠道实例。
func NewChatChannel(defaultDriver string) *ChatChannel {
	return &ChatChannel{
		drivers:       make(map[string]ChatDriver),
		defaultDriver: defaultDriver,
	}
}

// Name 返回渠道标识名。
func (c *ChatChannel) Name() string {
	return "chat"
}

// RegisterDriver 注册即时通讯底层传输驱动。
func (c *ChatChannel) RegisterDriver(name string, driver ChatDriver) *ChatChannel {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drivers[name] = driver
	if c.defaultDriver == "" {
		c.defaultDriver = name
	}
	return c
}

// Driver 获取指定驱动实例。
func (c *ChatChannel) Driver(name ...string) ChatDriver {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.drivers[cmp.Or(lo.FirstOr(name, ""), c.defaultDriver)]
}

// Format 将领域通知对象转换为即时通讯载荷 *ChatMessage。
func (c *ChatChannel) Format(n Notification, notifiable any) (any, error) {
	if chatNotif, ok := n.(ChatNotification); ok {
		return toChatMessage(chatNotif.ToChat(notifiable))
	}

	if cf, ok := n.(ChannelFormatter); ok {
		if raw := cf.Format(c.Name(), notifiable); raw != nil {
			return toChatMessage(raw)
		}
	}

	return nil, oops.Code(errs.CodeNotificationUnsupportedChat).
		With("notification", n).
		Errorf("notification does not implement ChatNotification or ChannelFormatter")
}

func toChatMessage(raw any) (*ChatMessage, error) {
	switch v := raw.(type) {
	case *ChatMessage:
		return v, nil
	case ChatMessage:
		return &v, nil
	case string:
		return NewChatMessage().WithText(v), nil
	default:
		return nil, oops.Code(errs.CodeInvalidChatMessage).Errorf("unsupported chat message payload type: %T", raw)
	}
}

// Send 执行即时通讯渠道的通知发送逻辑。
func (c *ChatChannel) Send(ctx context.Context, notifiable any, message any) error {
	msg, ok := message.(*ChatMessage)
	if !ok {
		return oops.Code(errs.CodeBadRequest).Errorf("chat: expected *ChatMessage, got %T", message)
	}

	if msg.Recipient == "" {
		if recipient := resolveChatRecipient(notifiable); recipient != "" {
			msg.To(recipient)
		}
	}

	driver := c.Driver()
	if driver == nil {
		return oops.Code(errs.CodeChatDriverNotFound).Errorf("chat: default driver %s is not registered", c.defaultDriver)
	}

	return driver.Send(ctx, msg)
}

func resolveChatRecipient(notifiable any) string {
	if notifiable == nil {
		return ""
	}
	target := ToNotifiable(notifiable)
	if target == nil {
		return ""
	}
	if recipient := target.RouteNotificationFor("chat"); recipient != "" {
		return recipient
	}
	return target.RouteNotificationFor("telegram")
}

// TelegramConfig Telegram 机器人驱动配置。
type TelegramConfig struct {
	BotToken string
	ChatID   string // 默认会话 ID
	BaseURL  string // 可自定义反向代理网关
	Timeout  time.Duration
}

// TelegramDriver 基于 Telegram Bot API 的即时通讯通知驱动实现。
type TelegramDriver struct {
	cfg    TelegramConfig
	client *resty.Client
}

var _ ChatDriver = (*TelegramDriver)(nil)

// NewTelegramDriver 创建 Telegram 驱动实例。
func NewTelegramDriver(cfg TelegramConfig) (*TelegramDriver, error) {
	if cfg.BotToken == "" {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("telegram: bot_token is required")
	}

	baseURL := cmp.Or(cfg.BaseURL, defaultTelegramBaseURL)
	timeout := cmp.Or(cfg.Timeout, defaultTelegramTimeout)

	client := resty.New().
		SetBaseURL(baseURL).
		SetTimeout(timeout)

	return &TelegramDriver{
		cfg:    cfg,
		client: client,
	}, nil
}

// Name 返回驱动标识名。
func (d *TelegramDriver) Name() string {
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
func (d *TelegramDriver) Send(ctx context.Context, msg *ChatMessage) error {
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
