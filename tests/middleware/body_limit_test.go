package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/middleware/global"
	"bgin/internal/platform/errs"
)

// TestBodyLimit 锁定请求体上限这条安全控制的两端：
// 超限的读取必须拿到 *http.MaxBytesError 并被映射成 413 REQUEST_TOO_LARGE，
// 上限之内的请求必须照常通过——否则这道限制要么形同虚设，要么误伤正常流量。
//
// 这里刻意不经由任何业务 handler 解码：被测的是「中间件施加限制 + errs 翻译该错误」这条契约，
// 与具体绑定实现无关。
func TestBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mw := global.New(global.Deps{})

	newEngine := func() *gin.Engine {
		engine := gin.New()
		engine.Use(mw.BodyLimit())
		engine.POST("/echo", func(c *gin.Context) {
			body, err := io.ReadAll(c.Request.Body)
			if err != nil {
				_ = c.Error(err)
				status, resp := errs.Map(err)
				c.AbortWithStatusJSON(status, resp)
				return
			}
			c.JSON(http.StatusOK, gin.H{"size": len(body)})
		})
		return engine
	}

	t.Run("超过上限返回 413", func(t *testing.T) {
		// 2 MiB，超过中间件设定的 1 MiB 上限。
		oversized := strings.Repeat("a", 2<<20)
		req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(oversized))
		rec := httptest.NewRecorder()

		newEngine().ServeHTTP(rec, req)

		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Contains(t, rec.Body.String(), string(errs.CodeRequestTooLarge))
	})

	t.Run("上限之内正常通过", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(`{"ok":true}`))
		rec := httptest.NewRecorder()

		newEngine().ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"size":11}`, rec.Body.String())
	})
}

// TestCacheBodyBypassMultipart 验证 CacheBody 对常规 JSON 正常缓存，对 multipart 上传请求旁路放行。
func TestCacheBodyBypassMultipart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mw := global.New(global.Deps{})

	engine := gin.New()
	engine.Use(mw.CacheBody())
	engine.POST("/check-cache", func(c *gin.Context) {
		_, cached := c.Get(gin.BodyBytesKey)
		c.JSON(http.StatusOK, gin.H{"cached": cached})
	})

	t.Run("常规 JSON 请求被正常缓存", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/check-cache", strings.NewReader(`{"test":123}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"cached":true}`, rec.Body.String())
	})

	t.Run("multipart 上传请求自动旁路不缓存", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/check-cache", strings.NewReader("fake multipart content"))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=test")
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"cached":false}`, rec.Body.String())
	})
}
