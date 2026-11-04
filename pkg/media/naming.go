package media

import (
	"path"
	"time"

	"github.com/google/uuid"
)

// CategoryPolicy 描述业务存储类目的安全配额与格式契约。
type CategoryPolicy struct {
	MaxFileSize       int64
	AllowedExtensions []string
	AllowedMimeTypes  []string
}

// DefaultCategoryPolicies 返回开箱即用的标准类目安全策略字典副本。
func DefaultCategoryPolicies() map[string]CategoryPolicy {
	return map[string]CategoryPolicy{
		"avatars": {
			MaxFileSize:       5 * 1024 * 1024, // 5MB
			AllowedExtensions: []string{string(FormatPNG), string(FormatJPG), string(FormatJPEG), string(FormatWebP), string(FormatGIF)},
			AllowedMimeTypes:  []string{"image/png", "image/jpeg", "image/webp", "image/gif"},
		},
		"covers": {
			MaxFileSize:       10 * 1024 * 1024, // 10MB
			AllowedExtensions: []string{string(FormatPNG), string(FormatJPG), string(FormatJPEG), string(FormatWebP)},
			AllowedMimeTypes:  []string{"image/png", "image/jpeg", "image/webp"},
		},
		"documents": {
			MaxFileSize:       50 * 1024 * 1024, // 50MB
			AllowedExtensions: []string{"pdf", "xlsx", "xls", "docx", "doc", "csv", "zip"},
			AllowedMimeTypes:  []string{"application/pdf", "application/vnd.openxmlformats-officedocument.*", "application/zip", "text/csv"},
		},
		"attachments": {
			MaxFileSize:       50 * 1024 * 1024, // 50MB
			AllowedExtensions: []string{"png", "jpg", "jpeg", "webp", "pdf", "zip", "txt"},
			AllowedMimeTypes:  []string{"image/*", "application/pdf", "application/zip", "text/plain"},
		},
	}
}

// NamingStrategy 定义对象存储路径（Key）的生成策略契约。
type NamingStrategy interface {
	Generate(category, fileName, mimeType string) string
}

type uuidv7NamingStrategy struct{}

// NewUUIDv7NamingStrategy 创建基于单调递增 UUIDv7 与 UTC 日期分区的默认命名策略。
// 格式形如：uploads/{category}/{YYYYMMDD}/{uuidv7}.{ext}
func NewUUIDv7NamingStrategy() NamingStrategy {
	return &uuidv7NamingStrategy{}
}

func (s *uuidv7NamingStrategy) Generate(category, fileName, mimeType string) string {
	ext := ResolveExtension(fileName, mimeType)
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	datePart := time.Now().UTC().Format("20060102")
	return path.Join("uploads", category, datePart, id.String()+ext)
}
