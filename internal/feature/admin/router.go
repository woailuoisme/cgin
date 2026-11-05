package admin

import (
	"github.com/gin-gonic/gin"

	authv1 "cgin/internal/feature/admin/auth/v1"
	userv1 "cgin/internal/feature/admin/user/v1"
	"cgin/internal/middleware"
)

// Handlers 聚合管理端各业务特性的 HTTP 处理器。
type Handlers struct {
	Auth *authv1.Handler
	User *userv1.Handler
}

// RegisterRoutes 将管理端所有业务特性的 v1 路由挂载至管理端路由器（如 /v1）。
func RegisterRoutes(v1 *gin.RouterGroup, h Handlers, mw *middleware.Middleware) {
	authv1.RegisterRoutes(v1, h.Auth, mw)
	userv1.RegisterRoutes(v1, h.User, mw)
}
