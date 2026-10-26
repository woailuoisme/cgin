// Package global 提供全局 HTTP 传输层中间件，挂载顺序见 internal/bootstrap/engine.go。
//
// 官方 gin-contrib 能覆盖的能力一律薄封装官方实现：
// CORS / Session / I18n / Timeout / RequestID / AccessLog。
// 官方生态没有对应实现、属项目自建（清理"重复造轮子"时不要误删）：
// ErrorHandler、Recovery、BodyLimit、EnforceJSONContentType、Idempotency、探针降噪。
package global

import (
	"cmp"
	"time"

	"github.com/gin-contrib/sessions"
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
	Translator i18n.Translator
	// Validator 用于把字段级校验失败翻译成当前语言；未注入时只返回原始文案。
	Validator      *validator.Validator
	RequestTimeout time.Duration
	// MaxBody 非 multipart 请求的请求体上限（字节，0 表示用默认值）。
	MaxBody int64
	// UploadMaxBody multipart/form-data 上传请求的请求体上限（字节，0 表示用默认值）。
	UploadMaxBody int64
	SessionStore  sessions.Store
	// SessionName 是会话 Cookie 名称；为空时回退 DefaultSessionName。
	SessionName string
	// SessionSecret 是会话签名密钥；为空时生成一次性随机密钥并告警。
	SessionSecret string
	// SessionMaxAge 是会话有效期；为 0 时回退 7 天。
	SessionMaxAge time.Duration
}

// Middleware 承载全局传输层中间件的实例依赖。
type Middleware struct {
	cache          cache.Store
	logger         *zerolog.Logger
	cors           config.CORSConfig
	production     bool
	translator     i18n.Translator
	validator      *validator.Validator
	requestTimeout time.Duration
	maxBody        int64
	uploadMaxBody  int64
	sessionStore   sessions.Store
	sessionName    string
	sessionSecret  string
	sessionMaxAge  time.Duration
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
		sessionStore:   deps.SessionStore,
		sessionName:    deps.SessionName,
		sessionSecret:  deps.SessionSecret,
		sessionMaxAge:  deps.SessionMaxAge,
	}
}

// abortWithError 向上下文附加错误并立即中止后续处理。
func abortWithError(c *gin.Context, err error) {
	_ = c.Error(err)
	c.Abort()
}
