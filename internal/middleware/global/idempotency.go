package global

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"cgin/internal/middleware/global/idempotency"
	"cgin/pkg/authentication"
	"cgin/pkg/cache"
)

const (
	// HeaderIdempotencyKey 是 IETF 标准幂等键请求头。
	HeaderIdempotencyKey = "Idempotency-Key"
	// defaultIdempotencyLockTTL 是分布式锁租期，需覆盖单次请求的 Handler 耗时。
	defaultIdempotencyLockTTL = 30 * time.Second
)

// Idempotency 构造全局挂载的请求幂等中间件。
//
// 归属：gin-contrib 官方无幂等重放控制，此处为项目自建能力。
func (m *Middleware) Idempotency(configs ...idempotency.Config) gin.HandlerFunc {
	return NewIdempotency(m.cache, configs...)
}

// NewIdempotency 构造独立实例；未注入 Storage/Lock 时回落到进程内实现，
// 注入平台缓存后响应回放与并发互斥在多实例间共享。
func NewIdempotency(store cache.Store, configs ...idempotency.Config) gin.HandlerFunc {
	var cfg idempotency.Config
	if len(configs) > 0 {
		cfg = configs[0]
	}

	if cfg.KeyHeader == "" {
		cfg.KeyHeader = HeaderIdempotencyKey
	}
	if cfg.KeyScope == nil {
		cfg.KeyScope = resolveIdempotencyScope
	}
	if cfg.Storage == nil && store != nil {
		cfg.Storage = idempotency.NewCacheStorage(store)
	}
	if cfg.Lock == nil && store != nil {
		cfg.Lock = idempotency.NewCacheLocker(store, defaultIdempotencyLockTTL)
	}
	return idempotency.New(cfg)
}

// IsFromCache 报告当前请求是否直接回放了幂等缓存。
func IsFromCache(c *gin.Context) bool {
	return idempotency.IsFromCache(c)
}

// WasPutToCache 报告当前请求的响应是否已写入幂等缓存。
func WasPutToCache(c *gin.Context) bool {
	return idempotency.WasPutToCache(c)
}

// resolveIdempotencyScope 解析幂等作用域：优先认证用户，其次凭据指纹，
// 再次显式会话/客户端标识，最后用客户端 IP 与 UA 的复合摘要兜底。
func resolveIdempotencyScope(c *gin.Context) string {
	if identity, ok := authentication.FromContext(c.Request.Context()); ok && identity.UserID != "" {
		return "user:" + identity.UserID
	}
	if fingerprint := credentialFingerprint(c.GetHeader("Authorization")); fingerprint != "" {
		return "credential:" + fingerprint
	}
	if sessionID := strings.TrimSpace(c.GetHeader("X-Session-ID")); sessionID != "" {
		return "session:" + shortHash(sessionID)
	}
	if clientID := strings.TrimSpace(c.GetHeader("X-Client-ID")); clientID != "" {
		return "client:" + shortHash(clientID)
	}
	return "anon:" + shortHash(c.ClientIP()+"\n"+c.Request.UserAgent())
}

// credentialFingerprint 对 Bearer 凭据本体取短摘要。
func credentialFingerprint(header string) string {
	credential := bearerCredential(header)
	if credential == "" {
		return ""
	}
	return shortHash(credential)
}

// bearerCredential 提取 Authorization 头中的凭据本体，缺失或非 Bearer 方案时返回空串。
func bearerCredential(header string) string {
	scheme, value, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}

// shortHash 返回输入 SHA-256 的前 16 字节十六进制摘要。
func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}
