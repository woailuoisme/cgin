package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/middleware/global"
)

// TestTimeoutMiddlewareWithinBudget 验证请求在超时预算内正常返回。
func TestTimeoutMiddlewareWithinBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := global.New(global.Deps{
		RequestTimeout: 50 * time.Millisecond,
	})

	router := gin.New()
	router.Use(m.ErrorHandler())
	router.Use(m.Timeout())

	router.GET("/fast", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/fast", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

// TestTimeoutMiddlewareExceeded 验证超出截止期时返回 504 GATEWAY_TIMEOUT。
func TestTimeoutMiddlewareExceeded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := global.New(global.Deps{
		RequestTimeout: 20 * time.Millisecond,
	})

	router := gin.New()
	router.Use(m.ErrorHandler())
	router.Use(m.Timeout())

	router.GET("/slow", func(c *gin.Context) {
		select {
		case <-time.After(100 * time.Millisecond):
			c.JSON(http.StatusOK, gin.H{"status": "late"})
		case <-c.Request.Context().Done():
			// 模拟下游感知到超时后主动退出
			return
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/slow", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusGatewayTimeout, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "GATEWAY_TIMEOUT", resp["code"])
}

// TestTimeoutMiddlewareWithExistingError 验证下游已附带超时错误时，不重复覆盖并能正确渲染 504。
func TestTimeoutMiddlewareWithExistingError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := global.New(global.Deps{
		RequestTimeout: 20 * time.Millisecond,
	})

	router := gin.New()
	router.Use(m.ErrorHandler())
	router.Use(m.Timeout())

	router.GET("/slow-err", func(c *gin.Context) {
		<-c.Request.Context().Done()
		_ = c.Error(c.Request.Context().Err())
	})

	req := httptest.NewRequest(http.MethodGet, "/slow-err", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "GATEWAY_TIMEOUT", resp["code"])
}

// TestTimeoutMiddleware_StreamingBypass 验证 WebSocket 升级与 SSE 流式请求自动豁免固定超时。
func TestTimeoutMiddleware_StreamingBypass(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := global.New(global.Deps{
		RequestTimeout: 20 * time.Millisecond,
	})

	router := gin.New()
	router.Use(m.ErrorHandler())
	router.Use(m.Timeout())

	router.GET("/stream", func(c *gin.Context) {
		// 验证没有被注入超时 context
		_, hasDeadline := c.Request.Context().Deadline()
		assert.False(t, hasDeadline, "流式长连接不应携带短超时截止期")
		c.JSON(http.StatusOK, gin.H{"status": "streaming"})
	})

	// 1. 验证 WebSocket Upgrade 标头豁免
	reqWS := httptest.NewRequest(http.MethodGet, "/stream", nil)
	reqWS.Header.Set("Upgrade", "websocket")
	wWS := httptest.NewRecorder()
	router.ServeHTTP(wWS, reqWS)
	require.Equal(t, http.StatusOK, wWS.Code)

	// 2. 验证 SSE Accept: text/event-stream 标头豁免
	reqSSE := httptest.NewRequest(http.MethodGet, "/stream", nil)
	reqSSE.Header.Set("Accept", "text/event-stream")
	wSSE := httptest.NewRecorder()
	router.ServeHTTP(wSSE, reqSSE)
	require.Equal(t, http.StatusOK, wSSE.Code)
}
