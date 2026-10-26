package global

import (
	"cmp"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"cgin/pkg/errs"
)

// maxBodyBytes 非上传请求的请求体大小上限（1 MiB），与 api.md 的 413 契约保持一致。
const maxBodyBytes = 1 << 20

// defaultUploadBodyBytes multipart 上传请求的兜底上限（32 MiB，与 gin 的默认内存阈值一致）。
const defaultUploadBodyBytes = 32 << 20

// BodyLimit 限制请求体大小；multipart 上传按 UploadMaxBody 单独放行，否则媒体上传会被普通上限提前拦掉。
// 官方无请求体配额中间件，属项目自建。
func (m *Middleware) BodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil {
			c.Next()
			return
		}

		limit := cmp.Or(m.maxBody, maxBodyBytes)
		if strings.HasPrefix(c.ContentType(), "multipart/form-data") {
			limit = cmp.Or(m.uploadMaxBody, defaultUploadBodyBytes)
		}

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}

// EnforceJSONContentType 要求 POST/PUT/PATCH 的实体载荷为 application/json。
//
// 旁路放行空载荷、文件/二进制上传与表单型 Webhook（如支付网关回调）；
// 分块传输（Content-Length 为 -1）仍进入校验，避免非标注入。官方无对应实现，属项目自建。
func (m *Middleware) EnforceJSONContentType() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			if c.Request.Body == nil || c.Request.ContentLength == 0 {
				c.Next()
				return
			}

			contentType := c.ContentType()
			// 旁路放行：文件/媒体上传、二进制流、表单通知（如支付网关）
			if strings.HasPrefix(contentType, "multipart/") ||
				contentType == "application/octet-stream" ||
				contentType == "application/x-www-form-urlencoded" {
				c.Next()
				return
			}

			if !strings.HasPrefix(contentType, "application/json") {
				abortWithError(c, errs.BadRequest("Content-Type must be application/json"))
				return
			}
		}

		c.Next()
	}
}
