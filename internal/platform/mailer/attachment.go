package mailer

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/samber/lo"
)

// Attachment 表示邮件附件，既支持纯内存字节流（如 PDF/Excel 生成），也兼容本地文件路径。
type Attachment struct {
	// Filename 附件名称（如 invoice.pdf）
	Filename string
	// Content 内存中的二进制内容（若 Content 非空，则优先使用 Content，无需磁盘临时文件）
	Content []byte
	// Path 本地文件系统路径（可选，兼容传统文件附件）
	Path string
	// ContentType MIME 媒体类型（如 application/pdf，可选）
	ContentType string
}

// NewAttachmentFromBytes 创建基于内存二进制字节流的附件对象（零磁盘 I/O）。
// 采用防御性拷贝隔离输入切片，遵循 Uber Go Style Guide 边界规范。
func NewAttachmentFromBytes(filename string, content []byte, contentType ...string) Attachment {
	return Attachment{
		Filename:    filename,
		Content:     slices.Clone(content),
		ContentType: lo.FirstOrEmpty(contentType),
	}
}

// NewAttachmentFromPath 创建基于本地文件系统路径的附件对象。
func NewAttachmentFromPath(path string, filename ...string) Attachment {
	return Attachment{
		Filename: cmp.Or(lo.FirstOr(filename, ""), filepath.Base(path)),
		Path:     path,
	}
}

// Bytes 获取附件的二进制字节数据；若由 Path 构造则动态从文件系统读取。
// 返回 Content 的防御性副本，防止外部调用方篡改底层字节数组。
func (a Attachment) Bytes() ([]byte, error) {
	if len(a.Content) > 0 {
		return slices.Clone(a.Content), nil
	}
	if a.Path != "" {
		return os.ReadFile(a.Path)
	}
	return nil, errors.New("mailer: attachment has neither content nor path")
}
