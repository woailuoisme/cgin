package config

import (
	"strings"
)

// MediaConfig 描述媒体文件上传、校验配额及 Imgproxy 图像处理服务配置。
type MediaConfig struct {
	// MaxFileSize 单文件上传大小上限（字节单位，默认 20MB）
	MaxFileSize int64 `mapstructure:"media_max_file_size"`
	// AllowedExtensions 允许上传的扩展名白名单（英文逗号分割，空表示不限制扩展名白名单）
	AllowedExtensions []string `mapstructure:"media_allowed_extensions"`
	// AllowedMimeTypes 允许上传的 MIME 类型白名单（英文逗号分割，空表示不限制 MIME 白名单）
	AllowedMimeTypes []string `mapstructure:"media_allowed_mime_types"`
	// DefaultCollection 默认媒体附件归属集合名
	DefaultCollection string `mapstructure:"media_default_collection"`

	// Imgproxy 动态图像处理服务配置
	Imgproxy ImgproxyConfig `mapstructure:",squash"`
}

// ImgproxyConfig 描述 Imgproxy 图像防篡改签名与服务端连接配置。
type ImgproxyConfig struct {
	// Enabled 控制是否启用 Imgproxy 图像处理服务 (默认 false)
	Enabled bool `mapstructure:"imgproxy_enabled"`
	// BaseURL Imgproxy 服务基础访问地址（如 http://localhost:8088）
	BaseURL string `mapstructure:"imgproxy_url"`
	// Key 十六进制编码的防篡改签名密钥
	Key string `mapstructure:"imgproxy_key"`
	// Salt 十六进制编码的防篡改签名加盐
	Salt string `mapstructure:"imgproxy_salt"`
	// Insecure 是否允许未签名或跳过防篡改签名验证 (默认 false)
	Insecure bool `mapstructure:"imgproxy_insecure"`
	// DefaultFormat 默认转换目标输出格式 (默认 webp)
	DefaultFormat string `mapstructure:"imgproxy_default_format"`
	// DefaultQuality 默认压缩质量 (1-100，默认 85)
	DefaultQuality int `mapstructure:"imgproxy_default_quality"`
}

// IsEnabled 报告是否启用了 Imgproxy 服务。
func (c *ImgproxyConfig) IsEnabled() bool {
	return c.Enabled && strings.TrimSpace(c.BaseURL) != ""
}

// ResolveCredentials 解析获取 Imgproxy BaseURL、Key 与 Salt。
func (c *ImgproxyConfig) ResolveCredentials() (baseURL, key, salt string) {
	baseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if baseURL == "" {
		baseURL = "http://localhost:8088"
	}
	return baseURL, strings.TrimSpace(c.Key), strings.TrimSpace(c.Salt)
}

// validateMedia 校验媒体与 Imgproxy 配置有效性。
func (c *Config) validateMedia() error {
	if c.Media.MaxFileSize <= 0 {
		c.Media.MaxFileSize = 20 * 1024 * 1024
	}
	if c.Media.Imgproxy.DefaultQuality <= 0 || c.Media.Imgproxy.DefaultQuality > 100 {
		c.Media.Imgproxy.DefaultQuality = 85
	}
	if c.Media.Imgproxy.DefaultFormat == "" {
		c.Media.Imgproxy.DefaultFormat = "webp"
	}
	return nil
}
