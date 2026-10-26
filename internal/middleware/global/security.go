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

// BodyLimit 【全局中间件】限制请求体大小上限，防止超大请求耗尽内存。
//
// multipart/form-data 上传按 UploadMaxBody（媒体配额）单独放行，
// 否则 20MB 的媒体上传会被 1 MiB 的普通请求上限提前拦掉。
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

// EnforceJSONContentType 【全局/路由中间件】对携带实体的变更请求（POST/PUT/PATCH）严格校验 Content-Type 为 application/json。
// 旁路放行规则：
// 1. 无 Body 或 Content-Length 为 0 的空载荷请求；
// 2. 文件或流式上传（multipart/form-data、application/octet-stream）；
// 3. 渠道外部 Webhook 异步回调（如支付网关携带 application/x-www-form-urlencoded）。
// 注意：分块传输（Transfer-Encoding: chunked，Content-Length 为 -1）按安全防御优先原则同样进入类型校验，杜绝非标注入。
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
