package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// defaultPaymentAppKey 是单应用便捷配置展开后的应用标识。
const defaultPaymentAppKey = "default"

// PaymentConfig 定义支付渠道配置，按「渠道 + 场景」解析应用凭据。
type PaymentConfig struct {
	// FakeEnabled 在非生产环境启用内存 Mock 渠道，用于本地测试。
	FakeEnabled bool `mapstructure:"payment_fake_enabled"`
	// CertsDir 支付凭据与证书文件的存放根目录。
	CertsDir string                     `mapstructure:"payment_certs_dir"`
	Wechat   WechatPaymentChannelConfig `mapstructure:",squash"`
	Alipay   AlipayPaymentChannelConfig `mapstructure:",squash"`
}

// WechatPaymentChannelConfig 定义微信支付渠道配置。
type WechatPaymentChannelConfig struct {
	// ApplicationsJSON 以 JSON 对象描述多个微信支付应用。
	ApplicationsJSON string `mapstructure:"wechat_pay_apps_json"`
	// Default 单应用便捷配置：未提供 ApplicationsJSON 时作为唯一应用生效。
	Default WechatPaymentAppConfig `mapstructure:",squash"`
}

// WechatPaymentAppConfig 定义单个微信支付应用的凭据、证书路径与支付场景。
type WechatPaymentAppConfig struct {
	// AppID 直连商户申请的公众号、小程序或开放平台移动应用 appid。
	AppID string `mapstructure:"wechat_pay_app_id" json:"app_id"`
	// MchID 微信支付商户号。
	MchID string `mapstructure:"wechat_pay_mch_id" json:"mch_id"`
	// MerchantSerialNo 商户 API 证书序列号。
	MerchantSerialNo string `mapstructure:"wechat_pay_mch_serial_no" json:"merchant_serial_no"`
	// APIv3Key APIv3 密钥，用于通知报文的 AES-256-GCM 解密与平台证书下载。
	APIv3Key string `mapstructure:"wechat_pay_api_v3_key" json:"api_v3_key"`
	// MerchantPrivateKeyPath 商户 API 证书私钥 apiclient_key.pem 的路径。
	MerchantPrivateKeyPath string `mapstructure:"wechat_pay_merchant_private_key_path" json:"merchant_private_key_path"`
	// PlatformPublicKeyPath 微信支付公钥文件路径（公钥模式）；留空则改用平台证书自动下载与轮换。
	PlatformPublicKeyPath string `mapstructure:"wechat_pay_platform_public_key_path" json:"platform_public_key_path"`
	// PlatformPublicKeyID 微信支付公钥 ID，公钥模式下必填。
	PlatformPublicKeyID string `mapstructure:"wechat_pay_platform_public_key_id" json:"platform_public_key_id"`
	// NotifyURL 该应用的支付与退款结果通知地址。
	NotifyURL string `mapstructure:"wechat_pay_notify_url" json:"notify_url"`
	// Scenes 该应用可承载的支付场景，取值见 enums.PaymentScene。
	Scenes []string `mapstructure:"wechat_pay_scenes" json:"scenes"`
}

// AlipayPaymentChannelConfig 定义支付宝渠道配置。
type AlipayPaymentChannelConfig struct {
	// ApplicationsJSON 以 JSON 对象描述多个支付宝应用。
	ApplicationsJSON string `mapstructure:"alipay_apps_json"`
	// Default 单应用便捷配置：未提供 ApplicationsJSON 时作为唯一应用生效。
	Default AlipayPaymentAppConfig `mapstructure:",squash"`
}

// AlipayPaymentAppConfig 定义单个支付宝应用的凭据、证书路径与支付场景。
type AlipayPaymentAppConfig struct {
	// AppID 支付宝应用 ID。
	AppID string `mapstructure:"alipay_app_id" json:"app_id"`
	// PrivateKeyPath 应用私钥 apiclient_key.pem 的路径。
	PrivateKeyPath string `mapstructure:"alipay_private_key_path" json:"private_key_path"`
	// AppCertPath 应用公钥证书 appCertPublicKey.crt 的路径。
	AppCertPath string `mapstructure:"alipay_app_cert_path" json:"app_cert_path"`
	// PublicCertPath 支付宝公钥证书 alipayCertPublicKey_RSA2.crt 的路径。
	PublicCertPath string `mapstructure:"alipay_public_cert_path" json:"public_cert_path"`
	// RootCertPath 支付宝根证书 alipayRootCert.crt 的路径。
	RootCertPath string `mapstructure:"alipay_root_cert_path" json:"root_cert_path"`
	// NotifyURL 该应用的支付与退款结果通知地址。
	NotifyURL string `mapstructure:"alipay_notify_url" json:"notify_url"`
	// ReturnURL 同步回跳地址，仅电脑网站与手机网站支付需要。
	ReturnURL string `mapstructure:"alipay_return_url" json:"return_url"`
	// Scenes 该应用可承载的支付场景，取值见 enums.PaymentScene。
	Scenes []string `mapstructure:"alipay_scenes" json:"scenes"`
}

// resolveApplications 解析渠道的最终应用映射表，优先使用 ApplicationsJSON，未配置时回退至 Default。
func resolveApplications[T any](
	applicationsJSON string,
	defaultApp T,
	declared func(T) bool,
	jsonVarName string,
) (map[string]T, error) {
	if raw := strings.TrimSpace(applicationsJSON); raw != "" {
		apps := make(map[string]T)
		if err := json.Unmarshal([]byte(raw), &apps); err != nil {
			return nil, fmt.Errorf("config: invalid %s: %w", jsonVarName, err)
		}

		return apps, nil
	}

	if !declared(defaultApp) {
		return map[string]T{}, nil
	}

	return map[string]T{defaultPaymentAppKey: defaultApp}, nil
}

// ResolveWechatApplications 解析微信支付渠道的最终应用集合。
func ResolveWechatApplications(channel WechatPaymentChannelConfig) (map[string]WechatPaymentAppConfig, error) {
	return resolveApplications(channel.ApplicationsJSON, channel.Default, wechatDefaultDeclared, "WECHAT_PAY_APPS_JSON")
}

// ResolveAlipayApplications 解析支付宝渠道的最终应用集合。
func ResolveAlipayApplications(channel AlipayPaymentChannelConfig) (map[string]AlipayPaymentAppConfig, error) {
	return resolveApplications(channel.ApplicationsJSON, channel.Default, alipayDefaultDeclared, "ALIPAY_APPS_JSON")
}

// HasWechatApplications 判断微信渠道是否配置了至少一个应用。
func HasWechatApplications(channel WechatPaymentChannelConfig) bool {
	return hasApplications(ResolveWechatApplications(channel))
}

// HasAlipayApplications 判断支付宝渠道是否配置了至少一个应用。
func HasAlipayApplications(channel AlipayPaymentChannelConfig) bool {
	return hasApplications(ResolveAlipayApplications(channel))
}

// hasApplications 判断解析结果是否至少含一个应用；解析失败按"未配置"处理。
func hasApplications[T any](apps map[string]T, err error) bool {
	return err == nil && len(apps) > 0
}

// PaymentConfigured 判断是否配置了至少一个渠道应用。
func PaymentConfigured(cfg *Config) bool {
	if cfg == nil {
		return false
	}

	return HasWechatApplications(cfg.Payment.Wechat) || HasAlipayApplications(cfg.Payment.Alipay)
}

// anyNonEmpty 判断这组字段里是否至少有一个被填写（去空白后非空）。
func anyNonEmpty(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}

	return false
}

// wechatDefaultDeclared 判断微信支付扁平单应用配置是否已被填写。
// 必须提供核心商户身份标识 (AppID/MchID) 或明确指定支付场景，避免因默认文件路径导致误判。
func wechatDefaultDeclared(app WechatPaymentAppConfig) bool {
	if len(app.Scenes) > 0 {
		return true
	}
	return anyNonEmpty(app.AppID, app.MchID)
}

// alipayDefaultDeclared 判断支付宝扁平单应用配置是否已被填写。
// 必须提供核心应用身份标识 (AppID) 或明确指定支付场景，避免因默认文件路径导致误判。
func alipayDefaultDeclared(app AlipayPaymentAppConfig) bool {
	if len(app.Scenes) > 0 {
		return true
	}
	return anyNonEmpty(app.AppID)
}

// validatePayment 校验支付配置的合法性。
func (c *Config) validatePayment() error {
	if _, err := ResolveWechatApplications(c.Payment.Wechat); err != nil {
		return err
	}
	if _, err := ResolveAlipayApplications(c.Payment.Alipay); err != nil {
		return err
	}
	return nil
}
