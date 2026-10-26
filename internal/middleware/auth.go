package middleware

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-contrib/authz"
	"github.com/gin-gonic/gin"

	"cgin/pkg/authentication"
	"cgin/pkg/errs"
)

// Auth 校验 Bearer 令牌并把认证身份注入上下文。
// 守卫可缺省（用默认守卫）、指定单个（"client"/"admin"）或按序回退（mw.Auth("admin", "client")）。
func (m *Middleware) Auth(guardNames ...string) gin.HandlerFunc {
	guards := parseGuards(guardNames)

	return func(c *gin.Context) {
		raw, err := bearerToken(c)
		if err != nil {
			abortWithError(c, err)
			return
		}

		if m.authManager == nil {
			abortWithError(c, errs.Internal(errors.New("middleware: auth manager is required")))
			return
		}

		reqCtx, err := m.authenticateWithManager(c.Request.Context(), raw, guards)
		if err != nil {
			abortWithError(c, err)
			return
		}

		if m.authz != nil {
			reqCtx = authentication.WithGate(reqCtx, m.authz)
		}
		c.Request = c.Request.WithContext(reqCtx)

		// 将已认证的角色信息注入 BasicAuth，使 gin-contrib/authz 原生自动提取 subject
		if id, ok := authentication.FromContext(reqCtx); ok && id.Role != "" {
			c.Request.SetBasicAuth(id.Role, "")
		}

		c.Next()
	}
}

// authenticateWithManager 基于注入的 AuthManager 解析守卫并验证令牌，支持多守卫顺序降级匹配。
func (m *Middleware) authenticateWithManager(ctx context.Context, raw string, guards []string) (context.Context, error) {
	if len(guards) == 0 {
		guard, err := m.authManager.Resolve()
		if err != nil {
			return nil, errs.Internal(err)
		}
		newCtx, _, authErr := guard.AuthenticateToken(ctx, raw)
		if authErr != nil {
			return nil, authErr
		}
		return newCtx, nil
	}

	var lastErr error
	for _, gName := range guards {
		guard, err := m.authManager.Resolve(gName)
		if err != nil {
			lastErr = errs.Internal(err)
			continue
		}

		newCtx, _, authErr := guard.AuthenticateToken(ctx, raw)
		if authErr == nil {
			return newCtx, nil
		}
		lastErr = authErr
	}

	if lastErr == nil {
		lastErr = errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	return nil, lastErr
}

// Authz 【路由/组中间件】基于官方 gin-contrib/authz 执行 Casbin RESTful 路径与 HTTP 方法访问控制。
func (m *Middleware) Authz() gin.HandlerFunc {
	if m.authz == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return authz.NewAuthorizer(m.authz.CasbinEnforcer())
}

// Can 按资源+动作做 Casbin 资源级鉴权。
// 官方 authz 只支持 URL+Method，无法表达资源语义，故保留自建（路径级鉴权由 Authz 承担）。
func (m *Middleware) Can(resource, action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := authentication.FromContext(c.Request.Context()); !ok {
			abortWithError(c, errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required"))
			return
		}

		if m.authz == nil {
			abortWithError(c, errs.Internal(errors.New("middleware: authz enforcer not configured")))
			return
		}

		if !m.authz.Can(c.Request.Context(), resource, action) {
			abortWithError(c, errs.Forbidden("You are not allowed to perform this action"))
			return
		}
		c.Next()
	}
}

// parseGuards 解析可能存在的逗号分隔或变长守卫名称。
func parseGuards(guardNames []string) []string {
	guards := make([]string, 0, len(guardNames))
	for _, gn := range guardNames {
		for part := range strings.SplitSeq(gn, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				guards = append(guards, part)
			}
		}
	}
	return guards
}

// bearerToken 从 Authorization 头提取 Bearer 令牌。
func bearerToken(c *gin.Context) (string, error) {
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	if header == "" {
		return "", errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}

	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", errs.Unauthorized(
			errs.CodeAuthTokenInvalid,
			"Authorization header must use the Bearer scheme",
		)
	}
	return parts[1], nil
}
