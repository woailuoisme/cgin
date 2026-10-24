package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/middleware/global"
)

// TestSecurityHeaders 锁定安全响应头这道防线，以及 HSTS 只在生产下发的边界。
func TestSecurityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serve := func(production bool) *httptest.ResponseRecorder {
		mw := global.New(global.Deps{Production: production})
		engine := gin.New()
		engine.Use(mw.SecurityHeaders())
		engine.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))
		return rec
	}

	t.Run("基础防护头始终下发", func(t *testing.T) {
		rec := serve(false)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
		assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
		assert.Equal(t, "none", rec.Header().Get("X-Permitted-Cross-Domain-Policies"))
		assert.Equal(t, "camera=(), microphone=(), geolocation=(), payment=()", rec.Header().Get("Permissions-Policy"))
	})

	t.Run("HSTS 只在生产下发", func(t *testing.T) {
		// 本地开发走 http：浏览器一旦记住 HSTS，后续 http 请求会被升级成 https 而连不上，
		// 且这个决定无法靠改服务端撤销。
		assert.Empty(t, serve(false).Header().Get("Strict-Transport-Security"))
		assert.Equal(t, "max-age=31536000; includeSubDomains; preload", serve(true).Header().Get("Strict-Transport-Security"))
	})
}

// TestRecovery 锁定 panic 边界的对外行为：状态码统一为 500，响应体不带任何内部细节。
func TestRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mw := global.New(global.Deps{})

	engine := gin.New()
	// 链序与生产一致：Recovery 只负责恢复并记日志，500 由更外层的 ErrorHandler 统一渲染；
	// 不挂 RequestID（它会把 Deps.Logger 交给 logger.WithContext，本测试未提供日志器）。
	engine.Use(mw.ErrorHandler(), mw.Recovery())
	engine.GET("/boom", func(_ *gin.Context) {
		panic("secret-internal-detail")
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "INTERNAL_SERVER_ERROR")
	assert.NotContains(t, rec.Body.String(), "secret-internal-detail",
		"panic 详情只能进日志，绝不能出现在响应体里")
}
