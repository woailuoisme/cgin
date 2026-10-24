// Package media 提供上传文件安全性与格式校验契约与实现。
package media

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/samber/lo"
)

// defaultDisallowedExtensions 默认高危文件扩展名黑名单（防止可执行脚本上传执行）。
var defaultDisallowedExtensions = []string{
	"php", "php3", "php4", "php5", "php7", "php8", "phtml", "phtm", "phar",
	"sh", "bash", "zsh", "exe", "bat", "cmd", "vbs", "vbe", "com", "msi",
	"pl", "cgi", "py", "pyc", "pyo", "asp", "aspx", "jsp", "jspx", "jspa",
	"htaccess", "htpasswd", "ini", "env",
}

// defaultDisallowedMimeTypes 默认高危可执行与脚本 MIME 类型黑名单。
var defaultDisallowedMimeTypes = []string{
	"application/x-dosexec",
	"application/x-executable",
	"application/x-sharedlib",
	"application/x-shellscript",
	"application/x-sh",
	"text/x-shellscript",
	"application/x-php",
	"text/x-php",
	"application/x-httpd-php",
}

// FileValidator 执行上传安全策略校验（大小上限、危险扩展名/MIME黑名单及可选的扩展名/MIME白名单）。
type FileValidator struct {
	MaxFileSize          int64
	DisallowedExtensions []string
	DisallowedMimeTypes  []string
	AllowedExtensions    []string
	AllowedMimeTypes     []string
}

// NewValidator 创建具有标准安全默认配置的 FileValidator 实例（默认上限 20MB）。
//
// 注意：HTTP 上传受传输层全局 BodyLimit 约束，本上限主要约束进程内直接调用场景。
func NewValidator() *FileValidator {
	return &FileValidator{
		MaxFileSize:          20 * 1024 * 1024, // 20 MB
		DisallowedExtensions: slices.Clone(defaultDisallowedExtensions),
		DisallowedMimeTypes:  slices.Clone(defaultDisallowedMimeTypes),
	}
}

// WithMaxFileSize 设置自定义最大文件字节限制。
func (v *FileValidator) WithMaxFileSize(size int64) *FileValidator {
	if size > 0 {
		v.MaxFileSize = size
	}
	return v
}

// WithAllowedExtensions 严格限制允许上传的扩展名白名单（无需带前导点号）。
func (v *FileValidator) WithAllowedExtensions(exts ...string) *FileValidator {
	v.AllowedExtensions = cleanSlice(exts, true)
	return v
}

// WithAllowedMimeTypes 严格限制允许上传的 MIME 类型白名单。
func (v *FileValidator) WithAllowedMimeTypes(mimeTypes ...string) *FileValidator {
	v.AllowedMimeTypes = cleanSlice(mimeTypes, false)
	return v
}

// WithDisallowedExtensions 设置自定义高危扩展名黑名单。
func (v *FileValidator) WithDisallowedExtensions(exts ...string) *FileValidator {
	v.DisallowedExtensions = cleanSlice(exts, true)
	return v
}

// WithDisallowedMimeTypes 设置自定义高危 MIME 类型黑名单。
func (v *FileValidator) WithDisallowedMimeTypes(mimeTypes ...string) *FileValidator {
	v.DisallowedMimeTypes = cleanSlice(mimeTypes, false)
	return v
}

// cleanSlice 清洗并过滤字符串切片（转小写、去除首尾空格与可选的前导点号）。
func cleanSlice(items []string, trimDot bool) []string {
	return lo.FilterMap(items, func(item string, _ int) (string, bool) {
		clean := strings.ToLower(strings.TrimSpace(item))
		if trimDot {
			clean = strings.TrimPrefix(clean, ".")
		}
		return clean, clean != ""
	})
}

// ValidateSize 仅校验文件字节数是否落在允许区间内。
// 供调用方在读取内容之前做前置拒绝，避免把超大文件整读进内存后才失败。
func (v *FileValidator) ValidateSize(size int64) error {
	if size <= 0 {
		return errors.New("invalid file: file size is empty (0 bytes)")
	}

	if v.MaxFileSize > 0 && size > v.MaxFileSize {
		return fmt.Errorf("file size %s (%d bytes) exceeds maximum limit of %s (%d bytes)",
			humanize.IBytes(uint64(size)), size,
			humanize.IBytes(uint64(v.MaxFileSize)), v.MaxFileSize)
	}
	return nil
}

// Validate 综合校验文件大小、危险扩展名/MIME黑名单及白名单规则。
func (v *FileValidator) Validate(_ context.Context, originalName string, size int64, mimeType string) error {
	if err := v.ValidateSize(size); err != nil {
		return err
	}

	baseName := strings.ToLower(filepath.Base(originalName))
	ext := strings.TrimPrefix(filepath.Ext(baseName), ".")

	// 校验危险扩展名黑名单
	if slices.Contains(v.DisallowedExtensions, ext) {
		return fmt.Errorf("file extension %q is forbidden for security reasons", ext)
	}

	// 校验多重后缀名中是否包含危险扩展名（防御如 attack.php.jpg 伪装）
	parts := strings.Split(baseName, ".")
	if len(parts) > 2 {
		for _, part := range parts[1 : len(parts)-1] {
			if slices.Contains(v.DisallowedExtensions, part) {
				return fmt.Errorf("file contains forbidden dangerous extension %q in filename", part)
			}
		}
	}

	// 校验高危 MIME 类型黑名单
	cleanMime := strings.ToLower(strings.TrimSpace(mimeType))
	if cleanMime != "" && slices.Contains(v.DisallowedMimeTypes, cleanMime) {
		return fmt.Errorf("file MIME type %q is forbidden for security reasons", cleanMime)
	}

	// 校验扩展名白名单（若已配置）
	if len(v.AllowedExtensions) > 0 && !slices.Contains(v.AllowedExtensions, ext) {
		return fmt.Errorf("file extension %q is not allowed; allowed: %v", ext, v.AllowedExtensions)
	}

	// 校验 MIME 类型白名单（若已配置）
	if len(v.AllowedMimeTypes) > 0 {
		matched := slices.ContainsFunc(v.AllowedMimeTypes, func(allowed string) bool {
			return cleanMime == allowed || strings.HasPrefix(cleanMime, strings.TrimSuffix(allowed, "*"))
		})
		if !matched {
			return fmt.Errorf("file MIME type %q is not allowed; allowed: %v", cleanMime, v.AllowedMimeTypes)
		}
	}

	return nil
}
