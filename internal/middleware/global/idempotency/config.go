// Package idempotency 提供 HTTP 请求幂等中间件。
//
// 语义：键缺省即透传 → 无锁读缓存 → 按 Key 加锁 → 锁内二次读 → 执行处理器 →
// 缓存响应；重复请求回放状态码、响应头与响应体，处理器失败时一律不缓存。
// 失败经 c.Error + c.Abort 表达，由全局 ErrorHandler 统一渲染。
package idempotency

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// ErrInvalidIdempotencyKey 表示幂等键未通过语法校验。
var ErrInvalidIdempotencyKey = errors.New("invalid idempotency key")

// Config 定义幂等中间件配置。
type Config struct {
	// Lock 串行化同一幂等键的并发请求，默认使用进程内 MemoryLock。
	Lock Locker
	// Storage 持久化已完成的响应，默认使用进程内 MemoryStorage。
	Storage Storage
	// Next 返回 true 时跳过幂等处理，默认跳过 HTTP 安全方法。
	Next func(c *gin.Context) bool
	// KeyHeaderValidate 校验幂等键语法，默认要求 36 字符（UUID 长度）。
	KeyHeaderValidate func(string) error
	// KeyScope 返回幂等键作用域；为空时仅用幂等键本身作为存储键。
	KeyScope func(c *gin.Context) string
	// KeyHeader 携带幂等键的请求头，默认 "Idempotency-Key"。
	KeyHeader string
	// KeepResponseHeaders 指定可回放的响应头；为空时保留全部响应头但排除 Cookie。
	KeepResponseHeaders []string
	// Lifetime 响应缓存与键的最大保留时长，默认 30 分钟。
	Lifetime time.Duration
	// DisableValueRedaction 为 true 时日志输出真实幂等键，默认脱敏。
	DisableValueRedaction bool
}

// ConfigDefault 是未显式配置时的默认值；Lock 与 Storage 在构造时按 Lifetime 初始化。
var ConfigDefault = Config{
	Next:              isSafeMethod,
	KeyHeaderValidate: validateKeyHeader,
	KeyHeader:         "Idempotency-Key",
	Lifetime:          30 * time.Minute,
}

// configDefault 合并用户配置与默认值，并补齐不可为空的依赖。
func configDefault(config ...Config) Config {
	cfg := ConfigDefault
	if len(config) > 0 {
		cfg = config[0]
	}

	if cfg.Next == nil {
		cfg.Next = ConfigDefault.Next
	}
	if cfg.KeyHeaderValidate == nil {
		cfg.KeyHeaderValidate = ConfigDefault.KeyHeaderValidate
	}
	if cfg.KeyHeader == "" {
		cfg.KeyHeader = ConfigDefault.KeyHeader
	}
	if cfg.Lifetime <= 0 {
		cfg.Lifetime = ConfigDefault.Lifetime
	}
	if cfg.KeepResponseHeaders != nil && len(cfg.KeepResponseHeaders) == 0 {
		cfg.KeepResponseHeaders = nil
	}
	if cfg.Lock == nil {
		cfg.Lock = NewMemoryLock()
	}
	if cfg.Storage == nil {
		cfg.Storage = NewMemoryStorage(cfg.Lifetime)
	}
	return cfg
}

// isSafeMethod 报告请求方法是否无副作用（GET/HEAD/OPTIONS/TRACE）。
func isSafeMethod(c *gin.Context) bool {
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

// validateKeyHeader 默认只校验长度是否等于 UUID 文本长度。
func validateKeyHeader(key string) error {
	if len(key) != 36 {
		return fmt.Errorf("%w: invalid length: %d != 36", ErrInvalidIdempotencyKey, len(key))
	}
	return nil
}
