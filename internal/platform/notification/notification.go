// Package notification 提供统一、模块化、高可扩展的多渠道通知基础设施。
package notification

import (
	"cmp"
	"context"
	"sync"

	"bgin/internal/config"
	"bgin/internal/platform/mailer"
	chatChannel "bgin/internal/platform/notification/channel/chat"
	telegramDriver "bgin/internal/platform/notification/channel/chat/telegram"
	gotifyChannel "bgin/internal/platform/notification/channel/gotify"
	gotifyHTTP "bgin/internal/platform/notification/channel/gotify/http"
	mailChannel "bgin/internal/platform/notification/channel/mail"
	rtChannel "bgin/internal/platform/notification/channel/realtime"
	smsChannel "bgin/internal/platform/notification/channel/sms"
	aliyunSMS "bgin/internal/platform/notification/channel/sms/aliyun"
	logSMS "bgin/internal/platform/notification/channel/sms/log"
	smsbaoSMS "bgin/internal/platform/notification/channel/sms/smsbao"
	tencentSMS "bgin/internal/platform/notification/channel/sms/tencent"
	"bgin/internal/platform/realtime"
)

var (
	defaultNotifier *Notifier
	notifierMu      sync.RWMutex
)

// SetDefaultManager 设置全局默认通知管理器。
func SetDefaultManager(n *Notifier) {
	notifierMu.Lock()
	defer notifierMu.Unlock()
	defaultNotifier = n
}

// DefaultManager 返回全局默认通知管理器。
func DefaultManager() *Notifier {
	notifierMu.RLock()
	defer notifierMu.RUnlock()
	return defaultNotifier
}

// New 根据配置与基础依赖构建统一多渠道通知引擎实例。
//
// 需要访问 shared 领域表的渠道（如 database）不在此处装配：平台层不得反向依赖
// shared 业务包，改由 bootstrap 构造后经 extra 参数注入。
func New(cfg config.NotificationConfig, m mailer.Mailer, rt realtime.Manager, extra ...Channel) (*Notifier, error) {
	poolSize := cmp.Or(cfg.WorkerPool, 10)
	queueSize := cmp.Or(cfg.QueueSize, 100)

	n := NewNotifier(poolSize, queueSize)

	// 1. 邮件渠道 (mail)
	if m != nil {
		n.Extend("mail", mailChannel.NewChannel(m))
	}

	// 2. 由调用方注入的渠道（database 等）
	for _, ch := range extra {
		if ch == nil {
			continue
		}
		n.Extend(ch.Name(), ch)
	}

	// 3. 实时广播渠道 (realtime)
	if rt != nil {
		n.Extend("realtime", rtChannel.NewChannel(rt))
	}

	// 4. 短信渠道 (sms)
	n.Extend("sms", buildSMSChannel(cfg))

	// 5. 即时通讯渠道 (chat - telegram)
	chatCh := chatChannel.NewChannel("telegram")
	if cfg.Telegram.BotToken != "" {
		tgDriver, err := telegramDriver.NewDriver(telegramDriver.Config{
			BotToken: cfg.Telegram.BotToken,
			ChatID:   cfg.Telegram.ChatID,
			BaseURL:  cfg.Telegram.APIURL,
		})
		if err == nil {
			chatCh.RegisterDriver("telegram", tgDriver)
		}
	}
	n.Extend("chat", chatCh)

	// 6. Gotify 实时推送渠道 (gotify)
	gotifyCh := gotifyChannel.NewChannel("http").
		WithDefaults(cfg.Gotify.ServerURL, cfg.Gotify.AppToken, cfg.Gotify.Priority)

	if cfg.Gotify.ServerURL != "" || cfg.Gotify.AppToken != "" {
		httpDriver, err := gotifyHTTP.NewDriver(gotifyHTTP.Config{
			ServerURL: cfg.Gotify.ServerURL,
			AppToken:  cfg.Gotify.AppToken,
			Priority:  cfg.Gotify.Priority,
		})
		if err == nil {
			gotifyCh.RegisterDriver("http", httpDriver)
		}
	}
	n.Extend("gotify", gotifyCh)

	SetDefaultManager(n)
	return n, nil
}

// Send 使用默认管理器向指定接收者发送通知。
func Send(ctx context.Context, notifiables any, notif Notification, opts ...SendOption) error {
	if mgr := DefaultManager(); mgr != nil {
		return mgr.Send(ctx, notifiables, notif, opts...)
	}
	return nil
}

// SendParallel 使用默认管理器并发发送通知。
func SendParallel(ctx context.Context, notifiables any, notif Notification, opts ...SendOption) error {
	if mgr := DefaultManager(); mgr != nil {
		return mgr.SendParallel(ctx, notifiables, notif, opts...)
	}
	return nil
}

// Use 为全局默认管理器注册管道中间件。
func Use(mw ...Middleware) {
	if mgr := DefaultManager(); mgr != nil {
		mgr.Use(mw...)
	}
}

// 类型别名以保持与现有业务领域的无缝兼容。
type (
	// RealtimeNotification 实时广播通知接口。
	RealtimeNotification = rtChannel.Notification
	// RealtimeMessage 实时广播消息载荷。
	RealtimeMessage = rtChannel.Message

	// MailNotification 邮件通知接口。
	MailNotification = mailChannel.Notification
	// MailMessage 邮件通知消息载荷。
	MailMessage = mailChannel.Message

	// SMSNotification 短信通知接口。
	SMSNotification = smsChannel.Notification
	// SMSMessage 短信通知消息载荷。
	SMSMessage = smsChannel.Message

	// ChatNotification 即时通讯通知接口。
	ChatNotification = chatChannel.Notification
	// ChatMessage 即时通讯通知消息载荷。
	ChatMessage = chatChannel.Message

	// GotifyNotification Gotify 推送通知接口。
	GotifyNotification = gotifyChannel.Notification
	// GotifyMessage Gotify 推送通知消息载荷。
	GotifyMessage = gotifyChannel.Message
)

// NewSMSMessage 创建短信载荷构建器。
func NewSMSMessage() *SMSMessage {
	return smsChannel.NewMessage()
}

// NewMailMessage 创建邮件载荷构建器。
func NewMailMessage() *MailMessage {
	return mailChannel.NewMessage()
}

// NewChatMessage 创建即时通讯载荷构建器。
func NewChatMessage() *ChatMessage {
	return chatChannel.NewMessage()
}

// NewGotifyMessage 创建 Gotify 载荷构建器。
func NewGotifyMessage(msg ...string) *GotifyMessage {
	return gotifyChannel.NewMessage(msg...)
}

// buildSMSChannel 按配置装配短信渠道并注册全部可用驱动。
// 驱动构造失败（凭据不完整）时静默跳过：配置校验已在启动期拦下必填项缺失。
func buildSMSChannel(cfg config.NotificationConfig) *smsChannel.Channel {
	smsCh := smsChannel.NewChannel(cmp.Or(cfg.SMS.NormalizedDriver(), "log"))
	smsCh.RegisterDriver("log", logSMS.NewDriver())

	if cfg.SMS.Aliyun.AccessKeyID != "" && cfg.SMS.Aliyun.AccessKeySecret != "" {
		driver, err := aliyunSMS.NewDriver(aliyunSMS.Config{
			AccessKeyID:     cfg.SMS.Aliyun.AccessKeyID,
			AccessKeySecret: cfg.SMS.Aliyun.AccessKeySecret,
			SignName:        cmp.Or(cfg.SMS.Aliyun.SignName, cfg.SMS.DefaultSignName),
			TemplateCode:    cfg.SMS.Aliyun.TemplateCode,
		})
		if err == nil {
			smsCh.RegisterDriver("aliyun", driver)
		}
	}

	if cfg.SMS.Tencent.SecretID != "" && cfg.SMS.Tencent.SecretKey != "" {
		driver, err := tencentSMS.NewDriver(tencentSMS.Config{
			SecretID:   cfg.SMS.Tencent.SecretID,
			SecretKey:  cfg.SMS.Tencent.SecretKey,
			AppID:      cfg.SMS.Tencent.AppID,
			SignName:   cmp.Or(cfg.SMS.Tencent.SignName, cfg.SMS.DefaultSignName),
			TemplateID: cfg.SMS.Tencent.TemplateID,
			Region:     cfg.SMS.Tencent.Region,
		})
		if err == nil {
			smsCh.RegisterDriver("tencent", driver)
		}
	}

	if cfg.SMS.SMSBao.Username != "" && cfg.SMS.SMSBao.APIKey != "" {
		driver, err := smsbaoSMS.NewDriver(smsbaoSMS.Config{
			Username: cfg.SMS.SMSBao.Username,
			APIKey:   cfg.SMS.SMSBao.APIKey,
			SignName: cmp.Or(cfg.SMS.SMSBao.SignName, cfg.SMS.DefaultSignName),
			Template: cfg.SMS.SMSBao.Template,
		})
		if err == nil {
			smsCh.RegisterDriver("smsbao", driver)
		}
	}

	return smsCh
}
