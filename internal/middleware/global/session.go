package global

import (
	"cmp"
	"net/http"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"cgin/internal/config"
)

// DefaultSessionName 是默认 Session Cookie 名称。
const DefaultSessionName = "cgin_session"

// defaultSessionMaxAge 是未配置有效期时的兜底（7 天）。
const defaultSessionMaxAge = 7 * 24 * time.Hour

// Session 基于 gin-contrib/sessions 提供会话管理。
// 密钥只来自配置层（生产必填）；仅有手工构造 Deps 时才会退化为一次性随机密钥并告警。
func (m *Middleware) Session(name ...string) gin.HandlerFunc {
	sessionName := m.sessionName
	if len(name) > 0 && name[0] != "" {
		sessionName = name[0]
	}
	sessionName = cmp.Or(sessionName, DefaultSessionName)

	maxAge := m.sessionMaxAge
	if maxAge <= 0 {
		maxAge = defaultSessionMaxAge
	}

	return sessions.Sessions(sessionName, m.sessionStoreOrCookie(maxAge))
}

// sessionStoreOrCookie 返回注入的会话存储；未注入时用配置密钥构建 Cookie 存储。
func (m *Middleware) sessionStoreOrCookie(maxAge time.Duration) sessions.Store {
	if m.sessionStore != nil {
		return m.sessionStore
	}

	secret := []byte(m.sessionSecret)
	if len(secret) == 0 {
		if generated, err := config.GenerateSessionSecret(); err == nil {
			secret = []byte(generated)
		}
		m.logger.Warn().
			Int("max_age_seconds", int(maxAge/time.Second)).
			Msg("session secret is empty, generated an ephemeral key: sessions will not survive a restart (set SESSION_SECRET)")
	}

	store := cookie.NewStore(secret)
	store.Options(sessions.Options{
		Path:     "/",
		HttpOnly: true,
		Secure:   m.production,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(maxAge / time.Second),
	})
	return store
}
