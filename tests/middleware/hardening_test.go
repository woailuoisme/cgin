package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/middleware/global"
)

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
