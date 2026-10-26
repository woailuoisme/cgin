// Package response 实现统一 HTTP 成功响应、Offset 分页信封与流式传输封装。
//
// 分页只提供 Offset 一种形态（api.md 6.1 的契约）：游标分页不在契约内，
// 待真有"深翻页性能不可接受"的端点时再按需引入，避免多出一套无人调用的响应形状。
package response

import (
	"cmp"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

// Meta 是传统偏移分页列表响应的元信息。
type Meta struct {
	Page       int `json:"page"`
	PageSize   int `json:"pageSize"`
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
}

// Page 是传统偏移分页列表的响应信封；Items 恒为数组，不会序列化成 null。
type Page[T any] struct {
	Items []T  `json:"items"`
	Meta  Meta `json:"meta"`
}

// NewMeta 计算传统分页元信息，totalPages 向上取整。
func NewMeta(page, pageSize, total int) Meta {
	var totalPages int
	if pageSize > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	return Meta{Page: page, PageSize: pageSize, Total: total, TotalPages: totalPages}
}

// OK 返回 200 与业务对象本身，不添加任何外层包装。
func OK[T any](c *gin.Context, data T) {
	c.JSON(http.StatusOK, data)
}

// Created 返回 201 与新建对象。
func Created[T any](c *gin.Context, data T) {
	c.JSON(http.StatusCreated, data)
}

// NoContent 返回 204，响应体为 0 字节。
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// Paginated 返回传统偏移分页信封，空列表同样保持结构完整。
func Paginated[T any](c *gin.Context, items []T, meta Meta) {
	// 为保持 RESTful JSON 契约一致性，将 nil 切片强制初始化为空切片 []T{}，避免在 JSON 序列化中产出 null。
	if items == nil {
		items = []T{}
	}
	c.JSON(http.StatusOK, Page[T]{Items: items, Meta: meta})
}

// Collection 返回无信封的数组集合，确保 nil 切片恒定序列化为 [] 而非 null。
func Collection[T any](c *gin.Context, items []T) {
	// 为保持 RESTful JSON 契约一致性，将 nil 切片强制初始化为空切片 []T{}，避免在 JSON 序列化中产出 null。
	if items == nil {
		items = []T{}
	}
	c.JSON(http.StatusOK, items)
}

// Attachment 将已知长度的流作为下载附件写回客户端，设置标准 Content-Disposition 并处理 RFC 5987 文件名。
// 长度未知的流式产出（如边写边生成的报表）请直接设置响应头后写 c.Writer。
func Attachment(c *gin.Context, filename string, r io.Reader, size int64, contentType ...string) {
	writeStream(c, dispositionAttachment, filename, r, size, contentType...)
}

// Inline 将已知长度的流作为内联展示（如 PDF/图片）写回客户端。
func Inline(c *gin.Context, filename string, r io.Reader, size int64, contentType ...string) {
	writeStream(c, dispositionInline, filename, r, size, contentType...)
}

// Content-Disposition 的两种处置方式。
const (
	dispositionAttachment = "attachment"
	dispositionInline     = "inline"
)

// writeStream 统一设置 RFC 5987 文件名与内容类型后写出流，attachment / inline 仅处置方式不同。
func writeStream(c *gin.Context, disposition, filename string, r io.Reader, size int64, contentType ...string) {
	ct := cmp.Or(lo.FirstOrEmpty(contentType), "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf("%s; filename=%q; filename*=UTF-8''%s",
		disposition, asciiFallbackFilename(filename), url.PathEscape(filename)))
	c.DataFromReader(http.StatusOK, size, ct, r, nil)
}

// asciiFallbackFilename 生成 Content-Disposition 的 ASCII 兜底名。
//
// filename 参数按 RFC 6266 只能承载 ASCII：非 ASCII 字符由 filename*= 用 UTF-8 承载。
// 直接 %q 会把中文名转义成 "\u8d26\u5355.pdf"，这里统一替换为下划线保持可读。
func asciiFallbackFilename(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)

	return cmp.Or(strings.TrimSpace(cleaned), "download")
}
