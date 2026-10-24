package middleware_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/middleware/global"
	"bgin/internal/platform/authentication"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/errs"
)

// memStore 是用于单元测试的内存级 IdempotencyStore 实现。
type memStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newMemStore() *memStore {
	return &memStore{data: make(map[string]string)}
}

func (s *memStore) SetNX(_ context.Context, key, value string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[key]; exists {
		return false, nil
	}
	s.data[key] = value
	return true, nil
}

func (s *memStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	val, exists := s.data[key]
	if !exists {
		return "", cache.ErrNotFound
	}
	return val, nil
}

func (s *memStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *memStore) Delete(_ context.Context, keys ...string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, k := range keys {
		if _, exists := s.data[k]; exists {
			delete(s.data, k)
			count++
		}
	}
	return count, nil
}

func setupTestRouter(store global.IdempotencyStore, opts ...global.IdempotencyOption) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	m := global.New(global.Deps{})
	r.Use(m.ErrorHandler())
	r.Use(global.NewIdempotency(store, opts...))
	return r
}

// TestIdempotency_PermissiveNoHeader 宽松模式下，未带幂等标头直接放行。
func TestIdempotency_PermissiveNoHeader(t *testing.T) {
	store := newMemStore()
	r := setupTestRouter(store)

	r.POST("/orders", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "created"})
	})

	req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewBufferString(`{"item":"book"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Header().Get("Idempotent-Replayed"))
}

// TestIdempotency_EnforcedNoHeader 严格模式下，未携带标头应被拦截并返回 400。
func TestIdempotency_EnforcedNoHeader(t *testing.T) {
	store := newMemStore()
	r := setupTestRouter(store, global.WithIdempotencyEnforced(true))

	r.POST("/orders", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "created"})
	})

	req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewBufferString(`{"item":"book"}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)

	var errResp map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &errResp)
	require.NoError(t, err)
	assert.Equal(t, string(errs.CodeIdempotencyRequired), errResp["code"])
}

// TestIdempotency_SuccessAndReplay 验证正常执行与相同请求直接回放。
func TestIdempotency_SuccessAndReplay(t *testing.T) {
	store := newMemStore()
	r := setupTestRouter(store)

	callCount := 0
	r.POST("/payments", func(c *gin.Context) {
		callCount++
		c.JSON(http.StatusCreated, gin.H{"order_id": "ord_123", "amount": 100})
	})

	body := `{"amount":100,"currency":"USD"}`

	// 第一次请求：真实执行
	req1 := httptest.NewRequest(http.MethodPost, "/payments", bytes.NewBufferString(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Idempotency-Key", "idem-uuid-001")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)

	require.Equal(t, http.StatusCreated, w1.Code)
	assert.Empty(t, w1.Header().Get("Idempotent-Replayed"))
	assert.Equal(t, 1, callCount)

	// 第二次请求：携带相同 Key 与 Body，应直接回放
	req2 := httptest.NewRequest(http.MethodPost, "/payments", bytes.NewBufferString(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Idempotency-Key", "idem-uuid-001")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	require.Equal(t, http.StatusCreated, w2.Code)
	assert.Equal(t, "true", w2.Header().Get("Idempotent-Replayed"))
	assert.Equal(t, w1.Body.String(), w2.Body.String())
	assert.Equal(t, 1, callCount, "后端 Handler 不应被再次调用")
}

// TestIdempotency_ScopeFollowsTokenSubject 锁定幂等作用域按令牌 sub 声明分片：
// 令牌轮换后同一幂等键仍应回放，而不同用户之间必须互相隔离。
func TestIdempotency_ScopeFollowsTokenSubject(t *testing.T) {
	store := newMemStore()
	r := setupTestRouter(store)

	callCount := 0
	r.POST("/subscriptions", func(c *gin.Context) {
		callCount++
		c.JSON(http.StatusCreated, gin.H{"subscription_id": "sub_1"})
	})

	body := `{"plan":"pro"}`
	key := "idem-rotation-001"

	first := postIdempotent(t, r, "/subscriptions", body, key, testJWT(t, "user-1"))
	require.Equal(t, http.StatusCreated, first.Code)

	// 令牌已轮换（签名与签发时间不同），但 sub 未变：必须回放而不是重复执行。
	rotated := postIdempotent(t, r, "/subscriptions", body, key, testJWT(t, "user-1"))
	require.Equal(t, http.StatusCreated, rotated.Code)
	assert.Equal(t, "true", rotated.Header().Get(global.HeaderIdempotentReplayed),
		"凭据轮换后同一用户的重试必须命中缓存")
	assert.Equal(t, 1, callCount)

	// 另一用户使用相同幂等键与请求体：作用域不同，必须真实执行。
	other := postIdempotent(t, r, "/subscriptions", body, key, testJWT(t, "user-2"))
	require.Equal(t, http.StatusCreated, other.Code)
	assert.Empty(t, other.Header().Get(global.HeaderIdempotentReplayed),
		"不同用户不得共享幂等命名空间")
	assert.Equal(t, 2, callCount)
}

// postIdempotent 发起一次带幂等键与 Bearer 凭据的 POST 请求。
func postIdempotent(t *testing.T, r *gin.Engine, path, body, key, token string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// testJWT 构造一个仅载荷有意义的 JWT：中间件只解析 sub，不做签名校验。
func testJWT(t *testing.T, subject string) string {
	t.Helper()

	encode := func(payload map[string]string) string {
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		return base64.RawURLEncoding.EncodeToString(raw)
	}

	return encode(map[string]string{"alg": "HS256", "typ": "JWT"}) + "." +
		encode(map[string]string{"sub": subject}) + ".signature"
}

// TestIdempotency_AltHeaderBridge 验证 X-Idempotency-Key 标头平滑兼容。
func TestIdempotency_AltHeaderBridge(t *testing.T) {
	store := newMemStore()
	r := setupTestRouter(store)

	r.POST("/charges", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"charge_id": "ch_888"})
	})

	body := `{"amount":50}`

	req1 := httptest.NewRequest(http.MethodPost, "/charges", bytes.NewBufferString(body))
	req1.Header.Set("X-Idempotency-Key", "legacy-key-999")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	require.Equal(t, http.StatusOK, w1.Code)

	req2 := httptest.NewRequest(http.MethodPost, "/charges", bytes.NewBufferString(body))
	req2.Header.Set("X-Idempotency-Key", "legacy-key-999")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	require.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "true", w2.Header().Get("Idempotent-Replayed"))
}

// TestIdempotency_PayloadMismatch 验证相同 Key 复用于不同载荷时返回 422。
func TestIdempotency_PayloadMismatch(t *testing.T) {
	store := newMemStore()
	r := setupTestRouter(store)

	r.POST("/transfers", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"transfer": "ok"})
	})

	// 第一次请求
	req1 := httptest.NewRequest(http.MethodPost, "/transfers", bytes.NewBufferString(`{"to":"Alice","amount":100}`))
	req1.Header.Set("Idempotency-Key", "transfer-key-1")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	require.Equal(t, http.StatusOK, w1.Code)

	// 第二次请求：改了接收人或金额
	req2 := httptest.NewRequest(http.MethodPost, "/transfers", bytes.NewBufferString(`{"to":"Bob","amount":999}`))
	req2.Header.Set("Idempotency-Key", "transfer-key-1")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	require.Equal(t, http.StatusUnprocessableEntity, w2.Code)

	var errResp map[string]any
	err := json.Unmarshal(w2.Body.Bytes(), &errResp)
	require.NoError(t, err)
	assert.Equal(t, string(errs.CodeIdempotencyMismatch), errResp["code"])
}

// TestIdempotency_InFlightConflict 验证同一 Key 在处理中发生并发时返回 409。
func TestIdempotency_InFlightConflict(t *testing.T) {
	store := newMemStore()
	r := setupTestRouter(store)

	// 预先向 store 注入处于 "processing" 状态的记录，模拟前序并发请求仍在执行中
	cacheKey := cache.Key("idempotency", "user:usr_concurrent", "inflight-key-123")
	processingRecord := `{"status":"processing","fingerprint":"any","created_at":123456789}`
	err := store.Set(t.Context(), cacheKey, processingRecord, 30*time.Second)
	require.NoError(t, err)

	r.POST("/heavy-task", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"done": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/heavy-task", bytes.NewBufferString(`{}`))
	req.Header.Set("Idempotency-Key", "inflight-key-123")
	req = req.WithContext(authentication.With(req.Context(), authentication.Identity{UserID: "usr_concurrent"}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code)

	var errResp map[string]any
	err = json.Unmarshal(w.Body.Bytes(), &errResp)
	require.NoError(t, err)
	assert.Equal(t, string(errs.CodeIdempotencyConflict), errResp["code"])
}

// TestIdempotency_ServerErrorDoesNotCache 验证 500 异常时不缓存，并自动删除锁以供重试。
func TestIdempotency_ServerErrorDoesNotCache(t *testing.T) {
	store := newMemStore()
	r := setupTestRouter(store)

	attempts := 0
	r.POST("/flaky", func(c *gin.Context) {
		attempts++
		if attempts == 1 {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db down"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "recovered"})
	})

	body := `{"param":"val"}`

	// 第一次调用：返回 500
	req1 := httptest.NewRequest(http.MethodPost, "/flaky", bytes.NewBufferString(body))
	req1.Header.Set("Idempotency-Key", "flaky-key-1")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	require.Equal(t, http.StatusInternalServerError, w1.Code)

	// 第二次调用：因为锁已释放，应当允许重试执行成功，而不是被 409 或 500 缓存阻断
	req2 := httptest.NewRequest(http.MethodPost, "/flaky", bytes.NewBufferString(body))
	req2.Header.Set("Idempotency-Key", "flaky-key-1")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	require.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, 2, attempts)
	assert.Empty(t, w2.Header().Get("Idempotent-Replayed"))
}

// TestIdempotency_UserScopeIsolation 验证不同用户使用相同 Idempotency-Key 互不干扰。
func TestIdempotency_UserScopeIsolation(t *testing.T) {
	store := newMemStore()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	m := global.New(global.Deps{})
	r.Use(m.ErrorHandler())

	// 模拟认证中间件：从 Header 取 X-Test-User-ID 注入 Identity 上下文
	r.Use(func(c *gin.Context) {
		if uid := c.GetHeader("X-Test-User-ID"); uid != "" {
			c.Request = c.Request.WithContext(authentication.With(c.Request.Context(), authentication.Identity{UserID: uid}))
		}
		c.Next()
	})
	r.Use(global.NewIdempotency(store))

	r.POST("/user-actions", func(c *gin.Context) {
		id, _ := authentication.FromContext(c.Request.Context())
		c.JSON(http.StatusOK, gin.H{"actor": id.UserID})
	})

	body := `{"action":"submit"}`
	sharedKey := "user-action-shared-key"

	// 用户 1 发送
	req1 := httptest.NewRequest(http.MethodPost, "/user-actions", bytes.NewBufferString(body))
	req1.Header.Set("Idempotency-Key", sharedKey)
	req1.Header.Set("X-Test-User-ID", "usr_111")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	require.Equal(t, http.StatusOK, w1.Code)
	assert.Contains(t, w1.Body.String(), "usr_111")

	// 用户 2 使用相同 Key 发送，应当正常执行，不得回放用户 1 的结果
	req2 := httptest.NewRequest(http.MethodPost, "/user-actions", bytes.NewBufferString(body))
	req2.Header.Set("Idempotency-Key", sharedKey)
	req2.Header.Set("X-Test-User-ID", "usr_222")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code)
	assert.Contains(t, w2.Body.String(), "usr_222")
	assert.Empty(t, w2.Header().Get("Idempotent-Replayed"))
}

// TestIdempotency_IgnoreSafeMethods 验证 GET/HEAD 请求不受幂等中间件影响。
func TestIdempotency_IgnoreSafeMethods(t *testing.T) {
	store := newMemStore()
	r := setupTestRouter(store, global.WithIdempotencyEnforced(true))

	r.GET("/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"alive": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

// TestIdempotency_SessionScopeIsolation 验证匿名客户端携带 X-Session-ID 时按会话隔离。
func TestIdempotency_SessionScopeIsolation(t *testing.T) {
	store := newMemStore()
	r := setupTestRouter(store)

	r.POST("/checkout", func(c *gin.Context) {
		c.JSON(http.StatusCreated, gin.H{"session": c.GetHeader("X-Session-ID")})
	})

	key := "shared-checkout-key"
	body := `{"cart_id":"cart_999"}`

	// 会话 A 请求
	reqA := httptest.NewRequest(http.MethodPost, "/checkout", bytes.NewBufferString(body))
	reqA.Header.Set("Content-Type", "application/json")
	reqA.Header.Set("Idempotency-Key", key)
	reqA.Header.Set("X-Session-ID", "sess_AAA")
	wA := httptest.NewRecorder()
	r.ServeHTTP(wA, reqA)
	require.Equal(t, http.StatusCreated, wA.Code)
	assert.Contains(t, wA.Body.String(), "sess_AAA")

	// 会话 B 使用相同幂等键：必须隔离，不应回放会话 A 的结果
	reqB := httptest.NewRequest(http.MethodPost, "/checkout", bytes.NewBufferString(body))
	reqB.Header.Set("Content-Type", "application/json")
	reqB.Header.Set("Idempotency-Key", key)
	reqB.Header.Set("X-Session-ID", "sess_BBB")
	wB := httptest.NewRecorder()
	r.ServeHTTP(wB, reqB)
	require.Equal(t, http.StatusCreated, wB.Code)
	assert.Contains(t, wB.Body.String(), "sess_BBB")
	assert.Empty(t, wB.Header().Get("Idempotent-Replayed"))
}

// TestIdempotency_MultipartSafe 验证 multipart/form-data 请求流式通过，正常完成且未因读取大体内存而崩溃。
func TestIdempotency_MultipartSafe(t *testing.T) {
	store := newMemStore()
	r := setupTestRouter(store)

	r.POST("/upload", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"uploaded": true})
	})

	multipartBody := "--boundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"test.txt\"\r\nContent-Type: text/plain\r\n\r\nhello world\r\n--boundary--\r\n"
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewBufferString(multipartBody))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	req.Header.Set("Idempotency-Key", "upload-key-001")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "true")
}
