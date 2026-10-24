package middleware_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/middleware/global"
)

type testPayload struct {
	Message string `json:"message"`
}

// TestCacheBodyMultiPassBinding 验证 CacheBody 中间件与 ShouldBindBodyWith 对多道绑定的支持。
func TestCacheBodyMultiPassBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := global.New(global.Deps{})

	router := gin.New()
	router.Use(m.CacheBody())

	var firstRead, secondRead testPayload
	var bodyKeyBytes []byte

	router.POST("/multi-pass", func(c *gin.Context) {
		// 第一次绑定：通过 ShouldBindBodyWith
		err := c.ShouldBindBodyWith(&firstRead, binding.JSON)
		if err != nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}

		// 第二次绑定：通过普通 ShouldBindJSON 验证流已被回写可复用
		err = c.ShouldBindJSON(&secondRead)
		if err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}

		// 验证 gin.BodyBytesKey 上下文缓存
		if cb, ok := c.Get(gin.BodyBytesKey); ok {
			if bytesVal, ok := cb.([]byte); ok {
				bodyKeyBytes = bytesVal
			}
		}

		c.Status(http.StatusOK)
	})

	body := []byte(`{"message":"hello gin"}`)
	req := httptest.NewRequest(http.MethodPost, "/multi-pass", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "hello gin", firstRead.Message)
	assert.Equal(t, "hello gin", secondRead.Message)
	assert.Equal(t, body, bodyKeyBytes)
}
