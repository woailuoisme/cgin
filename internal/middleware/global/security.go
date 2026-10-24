package global

import (
	"bytes"
	"cmp"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// maxBodyBytes 非上传请求的请求体大小上限（1 MiB），与 api.md 的 413 契约保持一致。
const maxBodyBytes = 1 << 20

// defaultUploadBodyBytes multipart 上传请求的兜底上限（32 MiB，与 gin 的默认内存阈值一致）。
const defaultUploadBodyBytes = 32 << 20

// CacheBody 【全局中间件】将普通请求体读取并缓存至上下文（gin.BodyBytesKey），避免下游多道读取流 EOF。
// 对 multipart/form-data 及流式大请求自动旁路放行，杜绝在 Go 堆上无休止分配大字节切片。
func (m *Middleware) CacheBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil || c.Request.ContentLength == 0 {
			c.Next()
			return
		}
		if _, ok := c.Get(gin.BodyBytesKey); ok {
			c.Next()
			return
		}

		// 流式或大文件上传直接旁路，保留底层流交给专有解析器
		contentType := c.ContentType()
		if strings.HasPrefix(contentType, "multipart/") || contentType == "application/octet-stream" {
			c.Next()
			return
		}

		limit := cmp.Or(m.maxBody, maxBodyBytes)
		if c.Request.ContentLength > limit {
			c.Next()
			return
		}

		// 防御性限制读取长度，防止无 Content-Length 的恶意 Chunked 流耗尽内存
		bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, limit+1))
		if err != nil {
			abortWithError(c, err)
			return
		}

		if int64(len(bodyBytes)) > limit {
			// 超出普通请求体上限：还原 body，交给下游 BodyLimit 中间件统一抛出 413
			c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(bodyBytes), c.Request.Body))
			c.Next()
			return
		}

		c.Set(gin.BodyBytesKey, bodyBytes)
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		c.Next()
	}
}

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

// SecurityHeaders 【全局中间件】注入常用安全响应头（防 MIME 嗅探、点击劫持、硬件探测与跨域文档滥用）。
func (m *Middleware) SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.Writer.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Permitted-Cross-Domain-Policies", "none")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		if m.production {
			header.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		}
		c.Next()
	}
}
