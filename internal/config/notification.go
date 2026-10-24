package config

import (
	"errors"
	"strings"
)

// NotificationConfig 描述多渠道通知系统配置。
type NotificationConfig struct {
	// WorkerPool 异步通知消费工作协程数量 (默认 10)
	WorkerPool int `mapstructure:"notification_worker_pool"`
	// QueueSize 异步通知缓冲队列容量 (默认 100)
	QueueSize int `mapstructure:"notification_queue_size"`

	// SMS 短信渠道配置
	SMS NotificationSMSConfig `mapstructure:",squash"`
	// Telegram 即时通讯渠道配置
	Telegram NotificationTelegramConfig `mapstructure:",squash"`
	// Gotify 推送通知渠道配置
	Gotify NotificationGotifyConfig `mapstructure:",squash"`
}

// NotificationGotifyConfig Gotify 实时推送渠道配置。
type NotificationGotifyConfig struct {
	ServerURL string `mapstructure:"notification_gotify_server_url"`
	AppToken  string `mapstructure:"notification_gotify_app_token"`
	Priority  int    `mapstructure:"notification_gotify_priority"`
}

// NotificationSMSConfig 短信渠道驱动与云厂商配置。
type NotificationSMSConfig struct {
	// Driver 短信发送驱动 (log: 控制台日志, mock: 内存模拟, aliyun: 阿里云, tencent: 腾讯云, smsbao: 短信宝)
	Driver string `mapstructure:"notification_sms_driver"`
	// DefaultSignName 默认全局短信签名
	DefaultSignName string `mapstructure:"notification_sms_sign_name"`

	// 阿里云短信配置
	Aliyun NotificationAliyunSMSConfig `mapstructure:",squash"`
	// 腾讯云短信配置
	Tencent NotificationTencentSMSConfig `mapstructure:",squash"`
	// 短信宝配置
	SMSBao NotificationSMSBaoConfig `mapstructure:",squash"`
}

// NotificationAliyunSMSConfig 阿里云短信访问凭据。
type NotificationAliyunSMSConfig struct {
	AccessKeyID     string `mapstructure:"notification_sms_aliyun_access_key_id"`
	AccessKeySecret string `mapstructure:"notification_sms_aliyun_access_key_secret"`
	SignName        string `mapstructure:"notification_sms_aliyun_sign_name"`
	TemplateCode    string `mapstructure:"notification_sms_aliyun_template_code"`
}

// NotificationTencentSMSConfig 腾讯云短信访问凭据。
type NotificationTencentSMSConfig struct {
	SecretID   string `mapstructure:"notification_sms_tencent_secret_id"`
	SecretKey  string `mapstructure:"notification_sms_tencent_secret_key"`
	AppID      string `mapstructure:"notification_sms_tencent_app_id"`
	SignName   string `mapstructure:"notification_sms_tencent_sign_name"`
	TemplateID string `mapstructure:"notification_sms_tencent_template_id"`
	Region     string `mapstructure:"notification_sms_tencent_region"`
}

// NotificationSMSBaoConfig 短信宝访问凭据。
type NotificationSMSBaoConfig struct {
	Username string `mapstructure:"notification_sms_smsbao_username"`
	APIKey   string `mapstructure:"notification_sms_smsbao_api_key"`
	SignName string `mapstructure:"notification_sms_smsbao_sign_name"`
	Template string `mapstructure:"notification_sms_smsbao_template"`
}

// NotificationTelegramConfig Telegram 即时通讯渠道配置。
type NotificationTelegramConfig struct {
	BotToken string `mapstructure:"notification_telegram_bot_token"`
	ChatID   string `mapstructure:"notification_telegram_chat_id"`
	APIURL   string `mapstructure:"notification_telegram_api_url"`
}

// debugSMSDrivers 是仅供开发调试的短信驱动集合。
// log 把手机号与验证码明文写进日志，mock 不会真正下发短信，两者在生产都不可接受。
var debugSMSDrivers = map[string]struct{}{
	"log":  {},
	"mock": {},
}

// NormalizedDriver 返回归一化后的短信驱动名（小写、去首尾空白）。
// 驱动名的比较、装配与落库只走这里，避免各处自行 TrimSpace/ToLower 产生分叉。
func (s *NotificationSMSConfig) NormalizedDriver() string {
	return strings.ToLower(strings.TrimSpace(s.Driver))
}

// Validate 校验通知配置合法性。
func (n *NotificationConfig) Validate() error {
	if n.WorkerPool <= 0 {
		return errors.New("notification worker pool must be greater than 0")
	}
	if n.QueueSize <= 0 {
		return errors.New("notification queue size must be greater than 0")
	}

	driver := n.SMS.NormalizedDriver()
	switch driver {
	case "", "log", "mock":
		// 开箱即用无需凭据
	case "aliyun":
		if strings.TrimSpace(n.SMS.Aliyun.AccessKeyID) == "" || strings.TrimSpace(n.SMS.Aliyun.AccessKeySecret) == "" {
			return errors.New("aliyun sms access key id and secret are required when driver is aliyun")
		}
	case "tencent":
		if strings.TrimSpace(n.SMS.Tencent.SecretID) == "" || strings.TrimSpace(n.SMS.Tencent.SecretKey) == "" || strings.TrimSpace(n.SMS.Tencent.AppID) == "" {
			return errors.New("tencent sms secret id, secret key and app id are required when driver is tencent")
		}
	case "smsbao":
		if strings.TrimSpace(n.SMS.SMSBao.Username) == "" || strings.TrimSpace(n.SMS.SMSBao.APIKey) == "" {
			return errors.New("smsbao username and api key are required when driver is smsbao")
		}
	default:
		return errors.New("unsupported notification sms driver: " + n.SMS.Driver)
	}

	return nil
}

// validateNotification 检查通知配置模块。
func (c *Config) validateNotification() error {
	if err := c.Notification.Validate(); err != nil {
		return err
	}

	if !c.IsProduction() {
		return nil
	}

	// 生产环境的短信驱动必须指向真实服务商：调试驱动一律拦下。
	driver := c.Notification.SMS.NormalizedDriver()
	if _, debugOnly := debugSMSDrivers[driver]; debugOnly {
		return errors.New("notification_sms_driver must be a real provider (aliyun/tencent/smsbao) in production")
	}

	// 完全不配置时归一为 none（不注册任何驱动），使用时按"驱动未注册"失败：
	// 既不会静默吞掉验证码，也不强制每个部署都必须接入短信。
	if driver == "" {
		c.Notification.SMS.Driver = "none"
	}
	return nil
}
