package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"bgin/internal/config"
	"bgin/internal/middleware/global"
)

// TestCORS_OptionsPreflightShortCircuit 验证 OPTIONS 预检请求返回 204 并且直接短路阻断后续中间件与路由。
func TestCORS_OptionsPreflightShortCircuit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := global.New(global.Deps{
		CORS: config.CORSConfig{
			AllowedOrigins: []string{"https://example.com"},
		},
	})

	downstreamExecuted := false
	router := gin.New()
	router.Use(m.CORS())
	router.Use(func(c *gin.Context) {
		downstreamExecuted = true
		c.Next()
	})
	router.POST("/api/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodOptions, "/api/test", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "https://example.com", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), "OPTIONS")
	assert.NotEmpty(t, w.Header().Get("Access-Control-Allow-Headers"))
	assert.False(t, downstreamExecuted, "OPTIONS preflight must short-circuit without executing downstream middleware")
}

// TestCORS_WildcardOrigin 验证通配符来源设置及非 OPTIONS 正常请求的头部回显。
func TestCORS_WildcardOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := global.New(global.Deps{
		CORS: config.CORSConfig{
			AllowedOrigins: []string{"*"},
		},
	})

	router := gin.New()
	router.Use(m.CORS())
	router.GET("/api/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Origin", "https://other.com")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"), "Wildcard origin must not allow credentials")
}
