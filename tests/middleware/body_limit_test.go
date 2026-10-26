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

	"cgin/internal/middleware/global"
	"cgin/pkg/errs"
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
