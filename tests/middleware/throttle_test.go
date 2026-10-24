package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/middleware"
	"bgin/internal/middleware/global"
	"bgin/internal/platform/authentication"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/errs"
)

// mockRateLimitStore 模拟内存限流存储。
type mockRateLimitStore struct {
	mu     sync.Mutex
	counts map[string]int64
	err    error
}

func newMockRateLimitStore() *mockRateLimitStore {
	return &mockRateLimitStore{
		counts: make(map[string]int64),
	}
}

func (s *mockRateLimitStore) Increment(_ context.Context, key string, _ time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	s.counts[key]++
	return s.counts[key], nil
}

func (s *mockRateLimitStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	val, ok := s.counts[key]
	if !ok {
		return "", cache.ErrNotFound
	}
	return strconv.FormatInt(val, 10), nil
}

// TestRateLimit_Allowed 验证在配额内请求被正常放行并附带标准限流响应头。
func TestRateLimit_Allowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newMockRateLimitStore()

	router := gin.New()
	router.Use(middleware.ThrottleWithStore(store, 10, time.Minute, middleware.WithThrottleTier(middleware.TierAnonymous)))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "10", w.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "9", w.Header().Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Reset"))
}

// TestRateLimit_Exceeded 验证超出配额时拦截为 429 并附带 Retry-After。
func TestRateLimit_Exceeded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newMockRateLimitStore()

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	router.Use(middleware.ThrottleWithStore(store, 2, time.Minute, middleware.WithThrottleTier(middleware.TierAnonymous)))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 前 2 次允许
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	}

	// 第 3 次超限 (limit = 2)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "2", w.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "0", w.Header().Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Reset"))
	assert.NotEmpty(t, w.Header().Get("Retry-After"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, string(errs.CodeRateLimited), body["code"])
}

// TestRateLimit_SlidingWindow 验证前一分钟窗口的加权余量计入当前估算。
func TestRateLimit_SlidingWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newMockRateLimitStore()

	// 预置前一个 bucket 的高频请求数 (600次，保证无论当前秒数偏移如何，平滑加权都大于限流阈值 5)
	now := time.Now().UTC()
	bucket := now.Unix() / 60
	prevKey := cache.Key("ratelimit", string(middleware.TierAnonymous), "ip:10.0.0.1", strconv.FormatInt(60, 10), strconv.FormatInt(bucket-1, 10))
	store.counts[prevKey] = 600

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	router.Use(middleware.ThrottleWithStore(store, 5, time.Minute, middleware.WithThrottleTier(middleware.TierAnonymous)))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 滑动窗口加权后应直接触发 429
	require.Equal(t, http.StatusTooManyRequests, w.Code)
}

// TestRateLimit_StoreFailOpen 验证缓存/存储异常时降级放行。
func TestRateLimit_StoreFailOpen(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newMockRateLimitStore()
	store.err = errors.New("redis connection refused")

	router := gin.New()
	router.Use(middleware.ThrottleWithStore(store, 5, time.Minute, middleware.WithThrottleTier(middleware.TierAnonymous)))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 存储故障时应当降级放行
	require.Equal(t, http.StatusOK, w.Code)
}

// TestRateLimit_NilStoreOrZeroLimit 验证存储为空或配额<=0时直接放行。
func TestRateLimit_NilStoreOrZeroLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. nil store
	router1 := gin.New()
	router1.Use(middleware.ThrottleWithStore(nil, 5, time.Minute, middleware.WithThrottleTier(middleware.TierAnonymous)))
	router1.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	w1 := httptest.NewRecorder()
	router1.ServeHTTP(w1, req1)
	require.Equal(t, http.StatusOK, w1.Code)

	// 2. 0 limit
	store := newMockRateLimitStore()
	router2 := gin.New()
	router2.Use(middleware.ThrottleWithStore(store, 0, time.Minute, middleware.WithThrottleTier(middleware.TierAnonymous)))
	router2.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	w2 := httptest.NewRecorder()
	router2.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code)
}

// TestRateLimit_AuthenticatedScope 验证认证用户维度按 UserID 隔离。
func TestRateLimit_AuthenticatedScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newMockRateLimitStore()

	router := gin.New()
	// 注入认证用户上下文
	router.Use(func(c *gin.Context) {
		ctx := authentication.With(c.Request.Context(), authentication.Identity{
			UserID: "usr_12345",
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.Use(middleware.ThrottleWithStore(store, 10, time.Minute, middleware.WithThrottleTier(middleware.TierAuthenticated)))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	// 验证存储中的 key 包含了 user:usr_12345
	now := time.Now().UTC()
	bucket := now.Unix() / 60
	expectedKey := cache.Key("ratelimit", string(middleware.TierAuthenticated), "user:usr_12345", strconv.FormatInt(60, 10), strconv.FormatInt(bucket, 10))
	assert.Equal(t, int64(1), store.counts[expectedKey])
}

// TestThrottle_CustomWindow 验证支持任意时间窗口（如 10 秒）。
func TestThrottle_CustomWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newMockRateLimitStore()

	router := gin.New()
	router.Use(middleware.ThrottleWithStore(store, 3, 10*time.Second))
	router.GET("/hourly", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/hourly", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "3", w.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "2", w.Header().Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Reset"))
}

// TestThrottle_CustomKey 验证支持自定义分区维度（如按登录账号 email 分区）。
func TestThrottle_CustomKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newMockRateLimitStore()

	router := gin.New()
	router.Use(middleware.ThrottleWithStore(
		store,
		5,
		time.Minute,
		middleware.WithThrottleKey(func(c *gin.Context) string {
			return "email:" + c.Query("email")
		}),
	))
	router.GET("/login-attempt", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/login-attempt?email=admin@example.com", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	now := time.Now().UTC()
	bucket := now.Unix() / 60
	expectedKey := cache.Key("ratelimit", "custom", "email:admin@example.com", strconv.FormatInt(60, 10), strconv.FormatInt(bucket, 10))
	assert.Equal(t, int64(1), store.counts[expectedKey])
}

// TestThrottle_Tier 验证通过 Middleware.Throttle 挂载特定业务档位（如 TierSensitive）。
func TestThrottle_Tier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newMockRateLimitStore()

	m := middleware.New(middleware.Deps{
		Limiter: config.LimiterConfig{
			Sensitive: 5,
		},
	})
	m.SetLimiterStore(store)

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	router.POST("/auth/login", m.Throttle(middleware.TierSensitive), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/login", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Limit"))
}

// TestThrottle_ThrottleRate 验证通过 Middleware.ThrottleRate 按自定义限制与周期限流。
func TestThrottle_ThrottleRate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newMockRateLimitStore()

	m := middleware.New(middleware.Deps{})
	m.SetLimiterStore(store)

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	// 允许每 1 分钟 2 次请求
	router.GET("/api/fast", m.ThrottleRate(2, time.Minute), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 第一次正常
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, httptest.NewRequest(http.MethodGet, "/api/fast", nil))
	assert.Equal(t, http.StatusOK, w1.Code)
	assert.Equal(t, "2", w1.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "1", w1.Header().Get("X-RateLimit-Remaining"))

	// 第二次正常
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/fast", nil))
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "0", w2.Header().Get("X-RateLimit-Remaining"))

	// 第三次触发限流 429
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, httptest.NewRequest(http.MethodGet, "/api/fast", nil))
	assert.Equal(t, http.StatusTooManyRequests, w3.Code)
}

// TestThrottle_CustomResponse 验证自定义超限拦截响应回调。
func TestThrottle_CustomResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newMockRateLimitStore()

	m := middleware.New(middleware.Deps{})
	m.SetLimiterStore(store)

	customOpt := middleware.WithThrottleResponse(func(c *gin.Context) {
		c.JSON(http.StatusTooManyRequests, gin.H{"custom_error": "please_wait"})
	})

	router := gin.New()
	router.GET("/custom", m.ThrottleRate(1, time.Minute, customOpt), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, httptest.NewRequest(http.MethodGet, "/custom", nil))
	assert.Equal(t, http.StatusOK, w1.Code)

	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/custom", nil))
	assert.Equal(t, http.StatusTooManyRequests, w2.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &body))
	assert.Equal(t, "please_wait", body["custom_error"])
}

// TestThrottle_DisabledGlobally 验证当 RATELIMIT_ENABLED=false 时，所有限流中间件完全旁路放行。
func TestThrottle_DisabledGlobally(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newMockRateLimitStore()

	// 显式将限流总开关设为 false
	m := middleware.New(middleware.Deps{
		Limiter: config.LimiterConfig{
			Enabled:       new(bool),
			Sensitive:     1,
			Anonymous:     1,
			Authenticated: 1,
		},
	})
	m.SetLimiterStore(store)

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	router.POST("/auth/login", m.Throttle(middleware.TierSensitive), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/rate", m.ThrottleRate(1, time.Minute), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 连续发起 5 次敏感登录请求，均应直接放行，且不注入限流响应头
	for i := range 5 {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/login", nil))
		assert.Equal(t, http.StatusOK, w.Code, "request %d should succeed", i+1)
		assert.Empty(t, w.Header().Get("X-RateLimit-Limit"), "headers should not be set when rate limit is disabled")
	}

	// 连续发起 5 次自定义速率请求，同样全量放行
	for i := range 5 {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/rate", nil))
		assert.Equal(t, http.StatusOK, w.Code, "request %d should succeed", i+1)
		assert.Empty(t, w.Header().Get("X-RateLimit-Limit"), "headers should not be set when rate limit is disabled")
	}
}
