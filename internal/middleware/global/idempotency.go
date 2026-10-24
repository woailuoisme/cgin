package global

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"bgin/internal/platform/authentication"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/logger"
)

const (
	// HeaderIdempotencyKey IETF / Stripe 标准幂等键标头名。
	HeaderIdempotencyKey = "Idempotency-Key"
	// AltHeaderIdempotencyKey 传统备用幂等键标头名。
	AltHeaderIdempotencyKey = "X-Idempotency-Key"
	// HeaderIdempotentReplayed 标记响应是否由幂等缓存直接回放。
	HeaderIdempotentReplayed = "Idempotent-Replayed"
	// DefaultIdempotencyLockTTL 默认并发锁定有效期（30秒）。
	DefaultIdempotencyLockTTL = 30 * time.Second
	// DefaultIdempotencyCacheTTL 默认成功响应缓存保留有效期（24小时）。
	DefaultIdempotencyCacheTTL = 24 * time.Hour
	// maxIdempotentBodyBytes 可被回放缓存的成功响应体上限（64 KiB）。
	// ponytail: 超过即不缓存（回放收益不值得把大列表响应压在缓存里）；
	// 需要回放大响应时改为存对象存储引用而非内联正文。
	maxIdempotentBodyBytes = 64 << 10
)

// IdempotencyConfig 定义请求幂等中间件配置选项。
type IdempotencyConfig struct {
	// Header 主幂等键标头，默认 "Idempotency-Key"。
	Header string
	// AltHeader 备用幂等键标头，默认 "X-Idempotency-Key"。
	AltHeader string
	// Enforce 是否强制要求必须携带幂等键，默认 false（宽松探测模式）。
	Enforce bool
	// LockTTL 处理中并发互斥锁的超时时长，默认 30 秒。
	LockTTL time.Duration
	// CacheTTL 处理成功响应的缓存保留时长，默认 24 小时。
	CacheTTL time.Duration
	// Methods 参与幂等控制的 HTTP 方法列表，默认 POST, PATCH。
	Methods []string
}

// IdempotencyOption 定义函数式配置项。
type IdempotencyOption func(*IdempotencyConfig)

// WithIdempotencyEnforced 设置是否开启强制校验模式。
func WithIdempotencyEnforced(enforced bool) IdempotencyOption {
	return func(c *IdempotencyConfig) {
		c.Enforce = enforced
	}
}

// WithIdempotencyLifetime 设置成功响应的缓存保留时长。
func WithIdempotencyLifetime(d time.Duration) IdempotencyOption {
	return func(c *IdempotencyConfig) {
		if d > 0 {
			c.CacheTTL = d
		}
	}
}

// WithIdempotencyLockTimeout 设置并发抢锁的过期时长。
func WithIdempotencyLockTimeout(d time.Duration) IdempotencyOption {
	return func(c *IdempotencyConfig) {
		if d > 0 {
			c.LockTTL = d
		}
	}
}

// WithIdempotencyMethods 指定受幂等性控制的 HTTP 方法集合。
func WithIdempotencyMethods(methods ...string) IdempotencyOption {
	return func(c *IdempotencyConfig) {
		if len(methods) > 0 {
			c.Methods = methods
		}
	}
}

type idempotencyStatus string

const (
	statusProcessing idempotencyStatus = "processing"
	statusCompleted  idempotencyStatus = "completed"
)

type idempotencyRecord struct {
	Status      idempotencyStatus `json:"status"`
	Fingerprint string            `json:"fingerprint"`
	StatusCode  int               `json:"statusCode,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        string            `json:"body,omitempty"`
	CreatedAt   int64             `json:"createdAt"`
}

type responseBufferWriter struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	statusCode int
}

func (w *responseBufferWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseBufferWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *responseBufferWriter) WriteString(s string) (int, error) {
	w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

func (w *responseBufferWriter) Status() int {
	if w.statusCode != 0 {
		return w.statusCode
	}
	return w.ResponseWriter.Status()
}

// IdempotencyStore 定义幂等中间件所需的存储原语接口。
// platform/cache.Client 结构体天然完整实现该接口。
type IdempotencyStore interface {
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) (int64, error)
}

// Idempotency 【全局中间件】基于 Middleware 依赖构造符合 IETF / Stripe 行业规范的 Gin 请求幂等中间件。
func (m *Middleware) Idempotency(opts ...IdempotencyOption) gin.HandlerFunc {
	return NewIdempotency(m.cache, opts...)
}

// NewIdempotency 构造独立的 Gin 幂等中间件实例，方便测试与自定义注入。
func NewIdempotency(store IdempotencyStore, opts ...IdempotencyOption) gin.HandlerFunc {
	cfg := defaultIdempotencyConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	methodsSet := make(map[string]struct{}, len(cfg.Methods))
	for _, method := range cfg.Methods {
		methodsSet[strings.ToUpper(method)] = struct{}{}
	}

	return func(c *gin.Context) {
		if _, ok := methodsSet[c.Request.Method]; !ok {
			c.Next()
			return
		}

		rawKey, valid := extractIdempotencyKey(c, &cfg)
		if !valid {
			return
		}
		if rawKey == "" || store == nil {
			c.Next()
			return
		}

		bodyBytes, err := readAndRewindRequestBody(c)
		if err != nil {
			abortWithError(c, errs.BadRequest("Failed to read request body"))
			return
		}

		fingerprint := computeRequestFingerprint(c.Request.Method, c.Request.URL.RequestURI(), c.ContentType(), bodyBytes)
		scope := resolveIdempotencyScope(c)
		cacheKey := cache.Key("idempotency", scope, rawKey)

		if !acquireOrHandleExisting(c, store, cacheKey, fingerprint, cfg.LockTTL) {
			return
		}

		bufferWriter := &responseBufferWriter{
			ResponseWriter: c.Writer,
			body:           bytes.NewBuffer(make([]byte, 0, 1024)),
		}
		c.Writer = bufferWriter

		defer handlePanicRelease(c.Request.Context(), store, cacheKey)

		c.Next()

		finishIdempotentRequest(c.Request.Context(), store, cacheKey, fingerprint, bufferWriter, cfg.CacheTTL)
	}
}

func defaultIdempotencyConfig() IdempotencyConfig {
	return IdempotencyConfig{
		Header:    HeaderIdempotencyKey,
		AltHeader: AltHeaderIdempotencyKey,
		Enforce:   false,
		LockTTL:   DefaultIdempotencyLockTTL,
		CacheTTL:  DefaultIdempotencyCacheTTL,
		Methods:   []string{http.MethodPost, http.MethodPatch},
	}
}

// extractIdempotencyKey 从请求中提取并验证幂等键。
func extractIdempotencyKey(c *gin.Context, cfg *IdempotencyConfig) (string, bool) {
	rawKey := strings.TrimSpace(c.GetHeader(cfg.Header))
	if rawKey == "" && cfg.AltHeader != "" {
		rawKey = strings.TrimSpace(c.GetHeader(cfg.AltHeader))
	}

	if rawKey == "" {
		if cfg.Enforce {
			abortWithError(c, errs.IdempotencyRequired(""))
			return "", false
		}
		return "", true
	}

	if len(rawKey) > 128 {
		abortWithError(c, errs.BadRequest("Idempotency key length exceeds 128 characters"))
		return "", false
	}

	return rawKey, true
}

// readAndRewindRequestBody 读取并重置 Request Body，便于指纹计算与后续绑定。
// 优先复用 CacheBody 已经读入的内存字节；对 multipart 上传请求避免全量缓存，直接返回 nil。
func readAndRewindRequestBody(c *gin.Context) ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}
	if strings.HasPrefix(c.ContentType(), "multipart/") {
		return nil, nil
	}
	if cached, ok := c.Get(gin.BodyBytesKey); ok {
		if bodyBytes, isBytes := cached.([]byte); isBytes {
			return bodyBytes, nil
		}
	}
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	return bodyBytes, nil
}

// acquireOrHandleExisting 尝试抢占锁；若键已存在则处理并发冲突、指纹比对或回放，返回 true 表示应继续执行。
func acquireOrHandleExisting(c *gin.Context, store IdempotencyStore, cacheKey, fingerprint string, lockTTL time.Duration) bool {
	ctx := c.Request.Context()
	lockRec := idempotencyRecord{
		Status:      statusProcessing,
		Fingerprint: fingerprint,
		CreatedAt:   time.Now().Unix(),
	}
	lockPayload, err := json.Marshal(lockRec)
	if err != nil {
		// 固定结构体实际不会失败；显式处理以免后人把这里当成可以吞错的先例。
		logger.FromContext(ctx).Error("failed to encode idempotency lock record", zap.Error(err), zap.String("key", cacheKey))
		c.Next()
		return false
	}

	acquired, err := store.SetNX(ctx, cacheKey, string(lockPayload), lockTTL)
	if err != nil {
		logger.FromContext(ctx).Error("idempotency cache SetNX failed", zap.Error(err), zap.String("key", cacheKey))
		c.Next()
		return false
	}
	if acquired {
		return true
	}

	existingVal, err := store.Get(ctx, cacheKey)
	if err != nil {
		if errors.Is(err, cache.ErrNotFound) {
			c.Next()
			return false
		}
		logger.FromContext(ctx).Error("failed to read idempotency cache", zap.Error(err), zap.String("key", cacheKey))
		c.Next()
		return false
	}

	handleExistingRecord(c, existingVal, fingerprint)
	return false
}

// handleExistingRecord 处理既有记录：判断是否在处理中、指纹是否吻合或执行响应回放。
func handleExistingRecord(c *gin.Context, existingVal, currentFingerprint string) {
	var existingRec idempotencyRecord
	if err := json.Unmarshal([]byte(existingVal), &existingRec); err != nil {
		logger.FromContext(c.Request.Context()).Error("failed to parse idempotency record", zap.Error(err))
		c.Next()
		return
	}

	if existingRec.Status == statusProcessing {
		abortWithError(c, errs.IdempotencyConflict(""))
		return
	}

	if existingRec.Fingerprint != currentFingerprint {
		abortWithError(c, errs.IdempotencyMismatch(""))
		return
	}

	replayIdempotentResponse(c, existingRec)
}

// replayIdempotentResponse 直接回放已缓存的响应结果。
func replayIdempotentResponse(c *gin.Context, rec idempotencyRecord) {
	for k, v := range rec.Headers {
		c.Header(k, v)
	}
	c.Header(HeaderIdempotentReplayed, "true")

	contentType := rec.Headers["Content-Type"]
	if contentType == "" {
		contentType = "application/json; charset=utf-8"
	}
	c.Data(rec.StatusCode, contentType, []byte(rec.Body))
	c.Abort()
}

// handlePanicRelease 发生 panic 崩溃时立即删除锁，确保客户端能够安全重试。
func handlePanicRelease(ctx context.Context, store IdempotencyStore, cacheKey string) {
	if r := recover(); r != nil {
		_, _ = store.Delete(ctx, cacheKey)
		panic(r)
	}
}

// finishIdempotentRequest 仅持久化 2xx 成功响应缓存；非 2xx、响应过大或未写入响应则删除锁以允许客户端重试。
func finishIdempotentRequest(ctx context.Context, store IdempotencyStore, cacheKey, fingerprint string, w *responseBufferWriter, ttl time.Duration) {
	finalStatusCode := w.Status()
	if finalStatusCode < http.StatusOK || finalStatusCode >= http.StatusMultipleChoices {
		_, _ = store.Delete(ctx, cacheKey)
		return
	}

	if w.body.Len() > maxIdempotentBodyBytes {
		logger.FromContext(ctx).Warn("idempotent response body exceeds cache limit, skipping replay cache",
			zap.String("key", cacheKey),
			zap.Int("body_bytes", w.body.Len()),
		)
		_, _ = store.Delete(ctx, cacheKey)
		return
	}

	headers := make(map[string]string)
	if ct := w.Header().Get("Content-Type"); ct != "" {
		headers["Content-Type"] = ct
	}

	completedRec := idempotencyRecord{
		Status:      statusCompleted,
		Fingerprint: fingerprint,
		StatusCode:  finalStatusCode,
		Headers:     headers,
		Body:        w.body.String(),
		CreatedAt:   time.Now().Unix(),
	}
	completedPayload, err := json.Marshal(completedRec)
	if err != nil {
		return
	}
	if err := store.Set(ctx, cacheKey, string(completedPayload), ttl); err != nil {
		logger.FromContext(ctx).Warn("failed to persist completed idempotency record", zap.Error(err), zap.String("key", cacheKey))
	}
}

// computeRequestFingerprint 对 Method、URI、Content-Type 与 Body 计算 SHA-256 哈希指纹。
func computeRequestFingerprint(method, uri, contentType string, body []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(method))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(uri))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(contentType))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// resolveIdempotencyScope 解析幂等缓存作用域（优先已认证 UserID，其次凭据指纹，再次客户端会话，兜底按客户端 IP 与 UA 复合散列分片）。
func resolveIdempotencyScope(c *gin.Context) string {
	if id, ok := authentication.FromContext(c.Request.Context()); ok && id.UserID != "" {
		return "user:" + id.UserID
	}
	if fingerprint := credentialFingerprint(c.GetHeader("Authorization")); fingerprint != "" {
		return "credential:" + fingerprint
	}
	if sessionID := strings.TrimSpace(c.GetHeader("X-Session-ID")); sessionID != "" {
		sum := sha256.Sum256([]byte(sessionID))
		return "session:" + hex.EncodeToString(sum[:16])
	}
	if clientID := strings.TrimSpace(c.GetHeader("X-Client-ID")); clientID != "" {
		sum := sha256.Sum256([]byte(clientID))
		return "client:" + hex.EncodeToString(sum[:16])
	}

	// 纯匿名无会话请求：结合 ClientIP 与 UserAgent 计算复合散列，最大程度避免纯 NAT IP 碰撞踩踏
	h := sha256.New()
	_, _ = h.Write([]byte(c.ClientIP()))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(c.Request.UserAgent()))
	sum := h.Sum(nil)
	return "anon:" + hex.EncodeToString(sum[:16])
}

// credentialFingerprint 对完整 Authorization 凭据（含签名段）取 SHA-256 短指纹。
func credentialFingerprint(header string) string {
	credential := bearerCredential(header)
	if credential == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:16])
}

// bearerCredential 提取 Authorization 头中的凭据本体，缺失或非 Bearer 方案时返回空串。
func bearerCredential(header string) string {
	scheme, value, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}
