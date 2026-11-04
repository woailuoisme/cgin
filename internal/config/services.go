package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// defaultAppKey 是单应用便捷配置展开后的默认应用标识。
const defaultAppKey = "default"

// resolveApps 泛型解析多应用 JSON 配置并在未显式覆盖 default 时合并单应用兜底配置。
func resolveApps[T any](
	appsJSON string,
	defaultApp T,
	declared func(T) bool,
	envName string,
) (map[string]T, error) {
	apps := make(map[string]T)
	if raw := strings.TrimSpace(appsJSON); raw != "" {
		if err := json.Unmarshal([]byte(raw), &apps); err != nil {
			return nil, fmt.Errorf("config: invalid %s: %w", envName, err)
		}
	}
	if declared(defaultApp) {
		if _, exists := apps[defaultAppKey]; !exists {
			apps[defaultAppKey] = defaultApp
		}
	}
	return apps, nil
}

// MailConfig 描述邮件发信服务配置，支持 mailpit 与 resend 驱动。
type MailConfig struct {
	Enabled     *bool  `env:"MAIL_ENABLED" envDefault:"true"`
	Driver      string `env:"MAIL_DRIVER" envDefault:"mailpit"`
	FromAddress string `env:"MAIL_FROM_ADDRESS" envDefault:"no-reply@cgin.local"`
	FromName    string `env:"MAIL_FROM_NAME" envDefault:"cgin"`
	Mailpit     MailpitConfig
	Resend      ResendConfig
}

// MailpitConfig 描述 Mailpit 驱动连接参数。
type MailpitConfig struct {
	Host string `env:"MAILPIT_HOST" envDefault:"127.0.0.1"`
	Port int    `env:"MAILPIT_PORT" envDefault:"1025"`
}

// ResendConfig 描述 Resend API 驱动配置。
type ResendConfig struct {
	APIKey  string `env:"RESEND_API_KEY"`
	BaseURL string `env:"RESEND_BASE_URL" envDefault:"https://api.resend.com"`
}

// IsEnabled 报告是否启用真实邮件发送服务。
func (m MailConfig) IsEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}

// Validate 检查邮件配置合法性。
func (m *MailConfig) Validate() error {
	if !m.IsEnabled() {
		return nil
	}

	switch strings.ToLower(strings.TrimSpace(m.Driver)) {
	case "mailpit", "":
		if m.Mailpit.Port <= 0 || m.Mailpit.Port > 65535 {
			return errors.New("mailpit port must be between 1 and 65535")
		}
	case "resend":
		if strings.TrimSpace(m.Resend.APIKey) == "" {
			return errors.New("resend api key cannot be empty when mail driver is resend")
		}
	default:
		return errors.New("unsupported mail driver: " + m.Driver + " (supported: 'mailpit', 'resend')")
	}
	return nil
}

// RealtimeConfig 描述 Centrifugo 实时通信服务连接配置。
type RealtimeConfig struct {
	Enabled  bool          `env:"CENTRIFUGO_ENABLED"`
	URL      string        `env:"CENTRIFUGO_URL"`
	APIKey   string        `env:"CENTRIFUGO_API_KEY"`
	Secret   string        `env:"CENTRIFUGO_SECRET"`
	TokenTTL time.Duration `env:"CENTRIFUGO_TOKEN_TTL" envDefault:"2h"`
}

// Validate 校验 Centrifugo 实时通信配置有效性。
func (r *RealtimeConfig) Validate() error {
	if !r.Enabled {
		return nil
	}
	if strings.TrimSpace(r.URL) == "" {
		return errors.New("config: CENTRIFUGO_URL cannot be empty when realtime is enabled")
	}
	if _, err := url.ParseRequestURI(r.URL); err != nil {
		return errors.New("config: invalid CENTRIFUGO_URL format")
	}
	if strings.TrimSpace(r.Secret) == "" {
		return errors.New("config: CENTRIFUGO_SECRET cannot be empty when realtime is enabled")
	}
	if strings.TrimSpace(r.APIKey) == "" {
		return errors.New("config: CENTRIFUGO_API_KEY cannot be empty when realtime is enabled")
	}
	return nil
}

// WeChatConfig 定义微信开放平台（公众平台 / 小程序）的服务端能力配置。
type WeChatConfig struct {
	ApplicationsJSON string `env:"WECHAT_MINIPROGRAM_APPS_JSON"`
	MiniProgram      WeChatMiniProgramConfig
}

// WeChatMiniProgramConfig 定义单个微信小程序服务端凭据。
type WeChatMiniProgramConfig struct {
	AppID     string `env:"WECHAT_MINIPROGRAM_APP_ID" json:"app_id"`
	AppSecret string `env:"WECHAT_MINIPROGRAM_APP_SECRET" json:"app_secret"`
}

// Declared 判断小程序凭据是否已被声明。
func (c WeChatMiniProgramConfig) Declared() bool {
	return strings.TrimSpace(c.AppID) != "" || strings.TrimSpace(c.AppSecret) != ""
}

// Declared 判断微信能力是否已配置（多应用 JSON 或单应用环境变量任一已声明即视为已配置）。
func (c *WeChatConfig) Declared() bool {
	return strings.TrimSpace(c.ApplicationsJSON) != "" || c.MiniProgram.Declared()
}

// Applications 解析并返回全部已声明的小程序配置映射。
func (c *WeChatConfig) Applications() (map[string]WeChatMiniProgramConfig, error) {
	return resolveApps(c.ApplicationsJSON, c.MiniProgram, WeChatMiniProgramConfig.Declared, "WECHAT_MINIPROGRAM_APPS_JSON")
}

// Validate 校验微信配置项的有效性。
func (c *WeChatConfig) Validate() error {
	if !c.Declared() {
		return nil
	}
	apps, err := c.Applications()
	if err != nil {
		return err
	}
	for name, app := range apps {
		if strings.TrimSpace(app.AppID) == "" {
			return fmt.Errorf("config: wechat miniprogram %q AppID cannot be empty", name)
		}
		if strings.TrimSpace(app.AppSecret) == "" {
			return fmt.Errorf("config: wechat miniprogram %q AppSecret cannot be empty", name)
		}
	}
	return nil
}

// PaymentConfig 定义支付渠道配置，按「渠道 + 场景」解析应用凭据。
type PaymentConfig struct {
	FakeEnabled bool   `env:"PAYMENT_FAKE_ENABLED"`
	CertsDir    string `env:"PAYMENT_CERTS_DIR" envDefault:"./storage/app/certs"`
	Production  bool
	Wechat      WechatPaymentChannelConfig
	Alipay      AlipayPaymentChannelConfig
}

// Normalize 注入运行环境上下文（如是否处于生产环境，用于阻断 Fake 渠道）。
func (p *PaymentConfig) Normalize(isProd bool) {
	p.Production = isProd
}

// Validate 校验支付渠道多应用配置的合法性。
func (p *PaymentConfig) Validate() error {
	_, wechatErr := p.Wechat.Applications()
	_, alipayErr := p.Alipay.Applications()
	return errors.Join(wechatErr, alipayErr)
}

// WechatPaymentChannelConfig 定义微信支付渠道配置。
type WechatPaymentChannelConfig struct {
	ApplicationsJSON string `env:"WECHAT_PAY_APPS_JSON"`
	Default          WechatPaymentAppConfig
}

// Applications 解析微信支付渠道的最终应用集合。
func (c WechatPaymentChannelConfig) Applications() (map[string]WechatPaymentAppConfig, error) {
	return resolveApps(c.ApplicationsJSON, c.Default, WechatPaymentAppConfig.Declared, "WECHAT_PAY_APPS_JSON")
}

// WechatPaymentAppConfig 定义单个微信支付应用的凭据、证书路径与支付场景。
type WechatPaymentAppConfig struct {
	AppID                  string   `env:"WECHAT_PAY_APP_ID" json:"app_id"`
	MchID                  string   `env:"WECHAT_PAY_MCH_ID" json:"mch_id"`
	MerchantSerialNo       string   `env:"WECHAT_PAY_MCH_SERIAL_NO" json:"merchant_serial_no"`
	APIv3Key               string   `env:"WECHAT_PAY_API_V3_KEY" json:"api_v3_key"`
	MerchantPrivateKeyPath string   `env:"WECHAT_PAY_MERCHANT_PRIVATE_KEY_PATH" json:"merchant_private_key_path"`
	PlatformPublicKeyPath  string   `env:"WECHAT_PAY_PLATFORM_PUBLIC_KEY_PATH" json:"platform_public_key_path"`
	PlatformPublicKeyID    string   `env:"WECHAT_PAY_PLATFORM_PUBLIC_KEY_ID" json:"platform_public_key_id"`
	NotifyURL              string   `env:"WECHAT_PAY_NOTIFY_URL" json:"notify_url"`
	Scenes                 []string `env:"WECHAT_PAY_SCENES" json:"scenes"`
}

// Declared 判断微信支付单应用配置是否已被填写。
func (a WechatPaymentAppConfig) Declared() bool {
	return len(a.Scenes) > 0 || strings.TrimSpace(a.AppID) != "" || strings.TrimSpace(a.MchID) != ""
}

// AlipayPaymentChannelConfig 定义支付宝渠道配置。
type AlipayPaymentChannelConfig struct {
	ApplicationsJSON string `env:"ALIPAY_APPS_JSON"`
	Default          AlipayPaymentAppConfig
}

// Applications 解析支付宝渠道的最终应用集合。
func (c AlipayPaymentChannelConfig) Applications() (map[string]AlipayPaymentAppConfig, error) {
	return resolveApps(c.ApplicationsJSON, c.Default, AlipayPaymentAppConfig.Declared, "ALIPAY_APPS_JSON")
}

// AlipayPaymentAppConfig 定义单个支付宝应用的凭据、证书路径与支付场景。
type AlipayPaymentAppConfig struct {
	AppID          string   `env:"ALIPAY_APP_ID" json:"app_id"`
	PrivateKeyPath string   `env:"ALIPAY_PRIVATE_KEY_PATH" json:"private_key_path"`
	AppCertPath    string   `env:"ALIPAY_APP_CERT_PATH" json:"app_cert_path"`
	PublicCertPath string   `env:"ALIPAY_PUBLIC_CERT_PATH" json:"public_cert_path"`
	RootCertPath   string   `env:"ALIPAY_ROOT_CERT_PATH" json:"root_cert_path"`
	NotifyURL      string   `env:"ALIPAY_NOTIFY_URL" json:"notify_url"`
	ReturnURL      string   `env:"ALIPAY_RETURN_URL" json:"return_url"`
	Scenes         []string `env:"ALIPAY_SCENES" json:"scenes"`
}

// Declared 判断支付宝单应用配置是否已被填写。
func (a AlipayPaymentAppConfig) Declared() bool {
	return len(a.Scenes) > 0 || strings.TrimSpace(a.AppID) != ""
}

// NotificationConfig 描述多渠道通知系统配置。
type NotificationConfig struct {
	WorkerPool int `env:"NOTIFICATION_WORKER_POOL" envDefault:"10"`
	QueueSize  int `env:"NOTIFICATION_QUEUE_SIZE" envDefault:"100"`
	SMS        NotificationSMSConfig
	Telegram   NotificationTelegramConfig
	Gotify     NotificationGotifyConfig
}

// NotificationGotifyConfig Gotify 实时推送渠道配置。
type NotificationGotifyConfig struct {
	ServerURL string `env:"NOTIFICATION_GOTIFY_SERVER_URL"`
	AppToken  string `env:"NOTIFICATION_GOTIFY_APP_TOKEN"`
	Priority  int    `env:"NOTIFICATION_GOTIFY_PRIORITY" envDefault:"5"`
}

// NotificationSMSConfig 短信渠道驱动与云厂商配置。
type NotificationSMSConfig struct {
	Driver          string `env:"NOTIFICATION_SMS_DRIVER"`
	DefaultSignName string `env:"NOTIFICATION_SMS_SIGN_NAME"`
	Aliyun          NotificationAliyunSMSConfig
	Tencent         NotificationTencentSMSConfig
	SMSBao          NotificationSMSBaoConfig
}

// NotificationAliyunSMSConfig 阿里云短信访问凭据。
type NotificationAliyunSMSConfig struct {
	AccessKeyID     string `env:"NOTIFICATION_SMS_ALIYUN_ACCESS_KEY_ID"`
	AccessKeySecret string `env:"NOTIFICATION_SMS_ALIYUN_ACCESS_KEY_SECRET"`
	SignName        string `env:"NOTIFICATION_SMS_ALIYUN_SIGN_NAME"`
	TemplateCode    string `env:"NOTIFICATION_SMS_ALIYUN_TEMPLATE_CODE"`
}

// NotificationTencentSMSConfig 腾讯云短信访问凭据。
type NotificationTencentSMSConfig struct {
	SecretID   string `env:"NOTIFICATION_SMS_TENCENT_SECRET_ID"`
	SecretKey  string `env:"NOTIFICATION_SMS_TENCENT_SECRET_KEY"`
	AppID      string `env:"NOTIFICATION_SMS_TENCENT_APP_ID"`
	SignName   string `env:"NOTIFICATION_SMS_TENCENT_SIGN_NAME"`
	TemplateID string `env:"NOTIFICATION_SMS_TENCENT_TEMPLATE_ID"`
	Region     string `env:"NOTIFICATION_SMS_TENCENT_REGION" envDefault:"ap-guangzhou"`
}

// NotificationSMSBaoConfig 短信宝访问凭据。
type NotificationSMSBaoConfig struct {
	Username string `env:"NOTIFICATION_SMS_SMSBAO_USERNAME"`
	APIKey   string `env:"NOTIFICATION_SMS_SMSBAO_API_KEY"`
	SignName string `env:"NOTIFICATION_SMS_SMSBAO_SIGN_NAME"`
	Template string `env:"NOTIFICATION_SMS_SMSBAO_TEMPLATE"`
}

// NotificationTelegramConfig Telegram 即时通讯渠道配置。
type NotificationTelegramConfig struct {
	BotToken string `env:"NOTIFICATION_TELEGRAM_BOT_TOKEN"`
	ChatID   string `env:"NOTIFICATION_TELEGRAM_CHAT_ID"`
	APIURL   string `env:"NOTIFICATION_TELEGRAM_API_URL" envDefault:"https://api.telegram.org"`
}

// NormalizedDriver 返回归一化后的短信驱动名（小写、去首尾空白）。
func (s *NotificationSMSConfig) NormalizedDriver() string {
	return strings.ToLower(strings.TrimSpace(s.Driver))
}

// Normalize 归一化短信驱动名称；生产环境未配置短信驱动时显式置为 none。
func (n *NotificationConfig) Normalize(isProd bool) {
	driver := n.SMS.NormalizedDriver()
	if isProd && driver == "" {
		n.SMS.Driver = "none"
	}
}

// Validate 校验通知配置合法性。
func (n *NotificationConfig) Validate(isProd bool) error {
	if n.WorkerPool <= 0 {
		return errors.New("notification worker pool must be greater than 0")
	}
	if n.QueueSize <= 0 {
		return errors.New("notification queue size must be greater than 0")
	}

	driver := n.SMS.NormalizedDriver()
	switch driver {
	case "", "none":
		return nil
	case "log", "mock":
		if isProd {
			return errors.New("notification_sms_driver must be a real provider (aliyun/tencent/smsbao) in production")
		}
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

// MediaConfig 描述媒体文件上传、校验配额及 Imgproxy 图像处理服务配置。
type MediaConfig struct {
	MaxFileSize       int64    `env:"MEDIA_MAX_FILE_SIZE" envDefault:"20971520"`
	AllowedExtensions []string `env:"MEDIA_ALLOWED_EXTENSIONS"`
	AllowedMimeTypes  []string `env:"MEDIA_ALLOWED_MIME_TYPES"`
	DefaultCollection string   `env:"MEDIA_DEFAULT_COLLECTION" envDefault:"default"`
	Imgproxy          ImgproxyConfig
}

// Normalize 归一化媒体上传配额、默认集合与内嵌 Imgproxy 参数。
func (m *MediaConfig) Normalize() {
	if m.MaxFileSize <= 0 {
		m.MaxFileSize = 20 * 1024 * 1024
	}
	m.DefaultCollection = cmp.Or(strings.TrimSpace(m.DefaultCollection), "default")
	m.Imgproxy.Normalize()
}

// ImgproxyConfig 描述 Imgproxy 图像防篡改签名与服务端连接配置。
type ImgproxyConfig struct {
	Enabled        bool   `env:"IMGPROXY_ENABLED"`
	BaseURL        string `env:"IMGPROXY_URL" envDefault:"http://localhost:8088"`
	InternalURL    string `env:"IMGPROXY_INTERNAL_URL"`
	Key            string `env:"IMGPROXY_KEY"`
	Salt           string `env:"IMGPROXY_SALT"`
	Insecure       bool   `env:"IMGPROXY_INSECURE"`
	DefaultFormat  string `env:"IMGPROXY_DEFAULT_FORMAT" envDefault:"webp"`
	DefaultQuality int    `env:"IMGPROXY_DEFAULT_QUALITY" envDefault:"85"`
}

// IsEnabled 报告是否启用了 Imgproxy 服务。
func (c *ImgproxyConfig) IsEnabled() bool {
	return c.Enabled && strings.TrimSpace(c.BaseURL) != ""
}

// Normalize 归一化 Imgproxy 地址、签名密钥与默认图像转换参数。
func (c *ImgproxyConfig) Normalize() {
	c.BaseURL = cmp.Or(strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"), "http://localhost:8088")
	c.InternalURL = cmp.Or(strings.TrimRight(strings.TrimSpace(c.InternalURL), "/"), c.BaseURL)
	c.Key = strings.TrimSpace(c.Key)
	c.Salt = strings.TrimSpace(c.Salt)
	c.DefaultFormat = cmp.Or(strings.TrimSpace(c.DefaultFormat), "webp")
	if c.DefaultQuality <= 0 || c.DefaultQuality > 100 {
		c.DefaultQuality = 85
	}
}

// PDFConfig 描述 PDF 生成服务的字体与页面参数。
type PDFConfig struct {
	FontPath   string  `env:"PDF_FONT_PATH" envDefault:"./storage/fonts/SourceHanSans.ttf"`
	FontFamily string  `env:"PDF_FONT_FAMILY" envDefault:"SourceHanSans"`
	PageSize   string  `env:"PDF_PAGE_SIZE" envDefault:"A4"`
	Margin     float64 `env:"PDF_MARGIN" envDefault:"10.0"`
}
