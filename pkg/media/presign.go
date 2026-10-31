// Package media 提供媒体文件上传、验证、处理与 Imgproxy 防篡改签名服务。
package media

import (
	"time"
)

// DefaultPresignTTL 预签名上传链接默认有效期（15分钟）。
const DefaultPresignTTL = 15 * time.Minute

// PresignRequest 封装申请预签名上传的请求参数。
type PresignRequest struct {
	Category string `json:"category"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	MimeType string `json:"mime_type"`
}

// PresignResult 封装生成的预签名上传凭据。
type PresignResult struct {
	UploadURL string            `json:"upload_url"`
	Path      string            `json:"path"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresIn int64             `json:"expires_in"` // 有效秒数
}
