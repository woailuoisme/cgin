// Package global 提供全局 HTTP 传输层中间件。
//
// 包含在 internal/bootstrap/engine.go 中全局生效的核心中间件：
//   - RequestID: 全链路请求 ID 提取与注入 (X-Request-ID)
//   - AccessLog: 结构化访问日志记录
//   - ErrorHandler: 集中捕获 c.Errors，多语言翻译并输出统一 JSON 错误信封
//   - Recovery: 未捕获 Panic 恢复与 500 兜底
//   - Timeout: 请求生命周期超时控制 (504 Gateway Timeout)
//   - SecurityHeaders: 浏览器安全标头注入 (nosniff, DENY, HSTS 等)
//   - BodyLimit: 请求体大小限制 (非上传 1MiB，上传 32MiB)
//   - CacheBody: 请求体流多道缓存 (gin.BodyBytesKey)
//   - Idempotency: HTTP 幂等性控制 (Idempotency-Key)
//   - CORS: 跨域资源共享策略与预检处理
package global

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/i18n"
)

// Deps 描述全局中间件所需的全部外部依赖。
type Deps struct {
	Cache          cache.Store
	Logger         *zap.Logger
	CORS           config.CORSConfig
	Production     bool
	Translator     *i18n.Translator
	RequestTimeout time.Duration
	// MaxBody 非 multipart 请求的请求体上限（字节，0 表示用默认值）。
	MaxBody int64
	// UploadMaxBody multipart/form-data 上传请求的请求体上限（字节，0 表示用默认值）。
	UploadMaxBody int64
}

// Middleware 承载全局传输层中间件的实例依赖。
type Middleware struct {
	cache          cache.Store
	logger         *zap.Logger
	cors           config.CORSConfig
	production     bool
	translator     *i18n.Translator
	requestTimeout time.Duration
	maxBody        int64
	uploadMaxBody  int64
}

// New 构造全局中间件实例。
func New(deps Deps) *Middleware {
	return &Middleware{
		cache:          deps.Cache,
		logger:         deps.Logger,
		cors:           deps.CORS,
		production:     deps.Production,
		translator:     deps.Translator,
		requestTimeout: deps.RequestTimeout,
		maxBody:        deps.MaxBody,
		uploadMaxBody:  deps.UploadMaxBody,
	}
}

// abortWithError 向上下文附加错误并立即中止后续处理。
func abortWithError(c *gin.Context, err error) {
	_ = c.Error(err)
	c.Abort()
}
