package middleware_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/middleware/global"
	"cgin/internal/middleware/global/idempotency"
	"cgin/internal/platform/errs"
)

// testIdempotencyKey 是符合默认 36 字符校验的幂等键。
const testIdempotencyKey = "019572d4-a6c8-78b1-8cf0-281b94c032a1"

// setupRouter 装配错误信封与幂等中间件；未注入存储时使用进程内实现。
func setupRouter(configs ...idempotency.Config) *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	middleware := global.New(global.Deps{})
	router.Use(middleware.ErrorHandler())
	router.Use(global.NewIdempotency(nil, configs...))
	return router
}

// doRequest 发起一次带幂等键的请求。
func doRequest(t *testing.T, router *gin.Engine, method, path, key, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Buffer
	if body != "" {
		reader = bytes.NewBufferString(body)
	} else {
		reader = bytes.NewBuffer(nil)
	}

	request := httptest.NewRequest(method, path, reader)
	if key != "" {
		request.Header.Set(global.HeaderIdempotencyKey, key)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// TestIdempotency_PassThroughWithoutKey 未携带幂等键时完全透传。
func TestIdempotency_PassThroughWithoutKey(t *testing.T) {
	router := setupRouter()
	var calls atomic.Int32

	router.POST("/orders", func(c *gin.Context) {
		calls.Add(1)
		c.JSON(http.StatusOK, gin.H{"status": "created"})
	})

	require.Equal(t, http.StatusOK, doRequest(t, router, http.MethodPost, "/orders", "", `{}`, nil).Code)
	require.Equal(t, http.StatusOK, doRequest(t, router, http.MethodPost, "/orders", "", `{}`, nil).Code)
	assert.EqualValues(t, 2, calls.Load())
}

// TestIdempotency_RejectsInvalidKey 默认校验要求 36 字符。
func TestIdempotency_RejectsInvalidKey(t *testing.T) {
	router := setupRouter()
	router.POST("/orders", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "created"})
	})

	recorder := doRequest(t, router, http.MethodPost, "/orders", "short-key", `{}`, nil)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), string(errs.CodeBadRequest))
}

// TestIdempotency_ReplaysCompletedResponse 重复请求回放状态码、响应头与响应体。
func TestIdempotency_ReplaysCompletedResponse(t *testing.T) {
	router := setupRouter()
	var calls atomic.Int32

	router.POST("/orders", func(c *gin.Context) {
		calls.Add(1)
		c.Header("X-Business-Trace", "trace-001")
		c.SetCookie("session", "secret", 3600, "/", "", false, true)
		c.JSON(http.StatusCreated, gin.H{"id": "ord_001"})
	})

	first := doRequest(t, router, http.MethodPost, "/orders", testIdempotencyKey, `{"item":"book"}`, nil)
	require.Equal(t, http.StatusCreated, first.Code)
	require.NotEmpty(t, first.Header().Get("Set-Cookie"))

	second := doRequest(t, router, http.MethodPost, "/orders", testIdempotencyKey, `{"item":"book"}`, nil)
	require.Equal(t, http.StatusCreated, second.Code)
	assert.JSONEq(t, first.Body.String(), second.Body.String())
	assert.Equal(t, "trace-001", second.Header().Get("X-Business-Trace"))
	assert.Empty(t, second.Header().Get("Set-Cookie"), "Cookie 默认不参与回放")
	assert.EqualValues(t, 1, calls.Load())
}

// TestIdempotency_ReplaysEmptyBodyStatus 无响应体时仍需回放状态码。
func TestIdempotency_ReplaysEmptyBodyStatus(t *testing.T) {
	router := setupRouter()
	var calls atomic.Int32

	router.POST("/no-content", func(c *gin.Context) {
		calls.Add(1)
		c.Status(http.StatusNoContent)
	})

	require.Equal(t, http.StatusNoContent, doRequest(t, router, http.MethodPost, "/no-content", testIdempotencyKey, `{}`, nil).Code)
	require.Equal(t, http.StatusNoContent, doRequest(t, router, http.MethodPost, "/no-content", testIdempotencyKey, `{}`, nil).Code)
	assert.EqualValues(t, 1, calls.Load())
}

// TestIdempotency_ConcurrentRequestsExecuteOnce 相同键并发时只执行一次，其余等待后回放。
func TestIdempotency_ConcurrentRequestsExecuteOnce(t *testing.T) {
	router := setupRouter()
	var calls atomic.Int32

	router.POST("/payments", func(c *gin.Context) {
		calls.Add(1)
		time.Sleep(40 * time.Millisecond)
		c.JSON(http.StatusOK, gin.H{"status": "paid"})
	})

	const workers = 6
	var waitGroup sync.WaitGroup
	codes := make([]int, workers)
	waitGroup.Add(workers)
	for index := range workers {
		go func() {
			defer waitGroup.Done()
			codes[index] = doRequest(t, router, http.MethodPost, "/payments", testIdempotencyKey, `{}`, nil).Code
		}()
	}
	waitGroup.Wait()

	for _, code := range codes {
		assert.Equal(t, http.StatusOK, code)
	}
	assert.EqualValues(t, 1, calls.Load())
}

// TestIdempotency_DoesNotCacheHandlerError 处理器返回错误时不缓存，允许重试。
func TestIdempotency_DoesNotCacheHandlerError(t *testing.T) {
	router := setupRouter()
	var calls atomic.Int32

	router.POST("/flaky", func(c *gin.Context) {
		if calls.Add(1) == 1 {
			_ = c.Error(errs.Internal(errors.New("database unavailable")))
			c.Abort()
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "recovered"})
	})

	first := doRequest(t, router, http.MethodPost, "/flaky", testIdempotencyKey, `{}`, nil)
	require.Equal(t, http.StatusInternalServerError, first.Code)

	second := doRequest(t, router, http.MethodPost, "/flaky", testIdempotencyKey, `{}`, nil)
	require.Equal(t, http.StatusOK, second.Code)
	assert.EqualValues(t, 2, calls.Load())
}

// TestIdempotency_KeyScopeIsolation 不同作用域使用同一幂等键互不干扰。
func TestIdempotency_KeyScopeIsolation(t *testing.T) {
	router := setupRouter(idempotency.Config{
		KeyScope: func(c *gin.Context) string {
			return c.GetHeader("X-Tenant")
		},
	})
	var calls atomic.Int32

	router.POST("/actions", func(c *gin.Context) {
		calls.Add(1)
		c.JSON(http.StatusOK, gin.H{"tenant": c.GetHeader("X-Tenant")})
	})

	first := doRequest(t, router, http.MethodPost, "/actions", testIdempotencyKey, `{}`, map[string]string{"X-Tenant": "tenant-a"})
	second := doRequest(t, router, http.MethodPost, "/actions", testIdempotencyKey, `{}`, map[string]string{"X-Tenant": "tenant-b"})

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	assert.Contains(t, first.Body.String(), "tenant-a")
	assert.Contains(t, second.Body.String(), "tenant-b")
	assert.EqualValues(t, 2, calls.Load())
}

// TestIdempotency_KeepResponseHeaders 显式白名单只回放列出的响应头。
func TestIdempotency_KeepResponseHeaders(t *testing.T) {
	router := setupRouter(idempotency.Config{KeepResponseHeaders: []string{"X-Keep"}})
	router.POST("/headers", func(c *gin.Context) {
		c.Header("X-Keep", "kept")
		c.Header("X-Drop", "dropped")
		c.SetCookie("session", "secret", 3600, "/", "", false, true)
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	require.Equal(t, http.StatusOK, doRequest(t, router, http.MethodPost, "/headers", testIdempotencyKey, `{}`, nil).Code)
	replay := doRequest(t, router, http.MethodPost, "/headers", testIdempotencyKey, `{}`, nil)

	assert.Equal(t, "kept", replay.Header().Get("X-Keep"))
	assert.Empty(t, replay.Header().Get("X-Drop"))
	assert.Empty(t, replay.Header().Get("Set-Cookie"))
}

// TestIdempotency_LifetimeExpiry 缓存过期后重新执行处理器。
func TestIdempotency_LifetimeExpiry(t *testing.T) {
	router := setupRouter(idempotency.Config{Lifetime: 30 * time.Millisecond})
	var calls atomic.Int32

	router.POST("/expiring", func(c *gin.Context) {
		calls.Add(1)
		c.JSON(http.StatusOK, gin.H{"call": calls.Load()})
	})

	require.Equal(t, http.StatusOK, doRequest(t, router, http.MethodPost, "/expiring", testIdempotencyKey, `{}`, nil).Code)
	time.Sleep(60 * time.Millisecond)
	require.Equal(t, http.StatusOK, doRequest(t, router, http.MethodPost, "/expiring", testIdempotencyKey, `{}`, nil).Code)
	assert.EqualValues(t, 2, calls.Load())
}

// TestIdempotency_SkipsSafeMethods 默认不拦截无副作用的 HTTP 方法。
func TestIdempotency_SkipsSafeMethods(t *testing.T) {
	router := setupRouter()
	var calls atomic.Int32

	router.GET("/status", func(c *gin.Context) {
		calls.Add(1)
		c.JSON(http.StatusOK, gin.H{"alive": true})
	})

	require.Equal(t, http.StatusOK, doRequest(t, router, http.MethodGet, "/status", testIdempotencyKey, "", nil).Code)
	require.Equal(t, http.StatusOK, doRequest(t, router, http.MethodGet, "/status", testIdempotencyKey, "", nil).Code)
	assert.EqualValues(t, 2, calls.Load())
}

// TestIdempotency_LockFailure 获取锁失败时返回 500。
func TestIdempotency_LockFailure(t *testing.T) {
	router := setupRouter(idempotency.Config{Lock: failingLocker{}})
	router.POST("/locked", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	recorder := doRequest(t, router, http.MethodPost, "/locked", testIdempotencyKey, `{}`, nil)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
}

// TestIdempotency_PublicHelpers 暴露写入缓存与缓存回放状态。
func TestIdempotency_PublicHelpers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	middleware := global.New(global.Deps{})
	router.Use(middleware.ErrorHandler())

	var fromCache, putToCache atomic.Bool
	router.Use(func(c *gin.Context) {
		c.Next()
		fromCache.Store(global.IsFromCache(c))
		putToCache.Store(global.WasPutToCache(c))
	})
	router.Use(global.NewIdempotency(nil))
	router.POST("/observe", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	require.Equal(t, http.StatusOK, doRequest(t, router, http.MethodPost, "/observe", testIdempotencyKey, `{}`, nil).Code)
	assert.False(t, fromCache.Load())
	assert.True(t, putToCache.Load())

	require.Equal(t, http.StatusOK, doRequest(t, router, http.MethodPost, "/observe", testIdempotencyKey, `{}`, nil).Code)
	assert.True(t, fromCache.Load())
	assert.False(t, putToCache.Load())
}

// failingLocker 是始终加锁失败的测试替身。
type failingLocker struct{}

// Lock 始终返回错误。
func (failingLocker) Lock(_ context.Context, _ string) error {
	return errors.New("lock backend unavailable")
}

// Unlock 无条件成功。
func (failingLocker) Unlock(_ context.Context, _ string) error {
	return nil
}
