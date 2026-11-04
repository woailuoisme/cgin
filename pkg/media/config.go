// Package media 提供媒体文件上传、验证、处理与 Imgproxy 防篡改签名服务。
package media

import "time"

// ImgproxyConfig 封装 Imgproxy 服务的通信与签名凭据配置。
type ImgproxyConfig struct {
	BaseURL        string
	InternalURL    string
	Key            string
	Salt           string
	Insecure       bool
	DefaultFormat  string
	DefaultQuality int
	Timeout        time.Duration
}

// SignerConfig 封装图像签名服务的完整构建配置。
type SignerConfig struct {
	Imgproxy    ImgproxyConfig
	StorageType string // "s3" | "local"
	S3Bucket    string
}

// UploaderConfig 封装预签名上传服务的构建配置。
type UploaderConfig struct {
	DefaultTTL        time.Duration
	MaxFileSize       int64
	AllowedExtensions []string
	AllowedMimeTypes  []string
	CategoryPolicies  map[string]CategoryPolicy
}
