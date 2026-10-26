// Package global 提供全局 HTTP 传输层中间件。
//
// 包含在 internal/bootstrap/engine.go 中全局生效的核心中间件：
//   - RequestID: 全链路请求 ID 提取与注入 (X-Request-ID)
//   - AccessLog: 结构化访问日志记录
//   - ErrorHandler: 集中捕获 c.Errors，多语言翻译并输出统一 JSON 错误信封
//   - Recovery: 未捕获 Panic 恢复与 500 兜底
//   - Timeout: 请求生命周期超时控制 (504 Gateway Timeout)
//   - BodyLimit: 请求体大小限制 (非上传 1MiB，上传 32MiB)
//   - EnforceJSONContentType: 变更请求 application/json 类型安全强校验
//   - Idempotency: HTTP 幂等性控制 (Idempotency-Key)
//   - CORS: 跨域资源共享策略与预检处理
package global

import (
	"cmp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"cgin/internal/config"
	"cgin/pkg/cache"
	"cgin/pkg/i18n"
	"cgin/pkg/logger"
	"cgin/pkg/validator"
)

// Deps 描述全局中间件所需的全部外部依赖。
type Deps struct {
	Cache      cache.Store
	Logger     *zerolog.Logger
	CORS       config.CORSConfig
	Production bool
	Translator *i18n.Translator
	// Validator 用于把字段级校验失败翻译成当前语言；未注入时只返回原始文案。
	Validator      *validator.Validator
	RequestTimeout time.Duration
	// MaxBody 非 multipart 请求的请求体上限（字节，0 表示用默认值）。
	MaxBody int64
	// UploadMaxBody multipart/form-data 上传请求的请求体上限（字节，0 表示用默认值）。
	UploadMaxBody int64
}

// Middleware 承载全局传输层中间件的实例依赖。
type Middleware struct {
	cache          cache.Store
	logger         *zerolog.Logger
	cors           config.CORSConfig
	production     bool
	translator     *i18n.Translator
	validator      *validator.Validator
	requestTimeout time.Duration
	maxBody        int64
	uploadMaxBody  int64
}

// New 构造全局中间件实例。
//
// 未注入 Logger 时回退到 logger.Nop()，使得各中间件可以无条件链式打日志。
func New(deps Deps) *Middleware {
	return &Middleware{
		cache:          deps.Cache,
		logger:         cmp.Or(deps.Logger, logger.Nop()),
		cors:           deps.CORS,
		production:     deps.Production,
		translator:     deps.Translator,
		validator:      deps.Validator,
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
