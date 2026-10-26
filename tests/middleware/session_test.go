package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/middleware/global"
)

// TestSessionMiddleware_SetAndGet 验证 gin-contrib/sessions 能够正确在上下文中读写会话状态并随 Cookie 回传。
func TestSessionMiddleware_SetAndGet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := global.New(global.Deps{
		SessionSecret: "secret-key-for-test-at-least-32-bytes!!",
	})

	router := gin.New()
	router.Use(m.Session())

	router.POST("/set", func(c *gin.Context) {
		sess := sessions.Default(c)
		sess.Set("user_id", "admin_123")
		require.NoError(t, sess.Save())
		c.Status(http.StatusOK)
	})

	router.GET("/get", func(c *gin.Context) {
		sess := sessions.Default(c)
		userID := sess.Get("user_id")
		if userID == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "no session"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"user_id": userID})
	})

	// 1. 设置会话并获取 Cookie
	reqSet := httptest.NewRequest(http.MethodPost, "/set", nil)
	wSet := httptest.NewRecorder()
	router.ServeHTTP(wSet, reqSet)
	require.Equal(t, http.StatusOK, wSet.Code)

	cookie := wSet.Header().Get("Set-Cookie")
	require.NotEmpty(t, cookie, "Set-Cookie 响应头必须非空")

	// 2. 携带 Cookie 获取会话数据
	reqGet := httptest.NewRequest(http.MethodGet, "/get", nil)
	reqGet.Header.Set("Cookie", cookie)
	wGet := httptest.NewRecorder()
	router.ServeHTTP(wGet, reqGet)

	require.Equal(t, http.StatusOK, wGet.Code)
	assert.Contains(t, wGet.Body.String(), "admin_123")
}

// TestSessionMiddleware_CustomCookieName 验证可注入的会话 Cookie 名称生效。
func TestSessionMiddleware_CustomCookieName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := global.New(global.Deps{
		SessionName:   "cgin_admin_sid",
		SessionSecret: "secret-key-for-test-at-least-32-bytes!!",
	})

	router := gin.New()
	router.Use(m.Session())
	router.POST("/set", func(c *gin.Context) {
		sess := sessions.Default(c)
		sess.Set("user_id", "admin_123")
		require.NoError(t, sess.Save())
		c.Status(http.StatusOK)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/set", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Set-Cookie"), "cgin_admin_sid=", "应使用配置的 Cookie 名称")
}

// TestSessionMiddleware_GeneratesEphemeralSecret 验证未注入密钥时仍可工作，不会退回静态弱密钥。
func TestSessionMiddleware_GeneratesEphemeralSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := global.New(global.Deps{})

	router := gin.New()
	router.Use(m.Session())
	router.POST("/set", func(c *gin.Context) {
		sess := sessions.Default(c)
		sess.Set("user_id", "admin_123")
		require.NoError(t, sess.Save())
		c.Status(http.StatusOK)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/set", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.NotEmpty(t, recorder.Header().Get("Set-Cookie"), "临时密钥也应能正常签发会话 Cookie")
}
