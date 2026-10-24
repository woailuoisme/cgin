package middleware

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"bgin/internal/platform/authentication"
	"bgin/internal/platform/errs"
	platformsentry "bgin/internal/platform/sentry"
)

// Auth 【路由/路由组中间件】仿照 Laravel auth 中间件设计，校验 Bearer 访问令牌并注入认证身份载荷。
// 支持未指定（使用默认守卫）、单个守卫（如 "client"、"admin"）以及多守卫顺序回退（如 mw.Auth("admin", "client") 或 mw.Auth("admin,client")）。
func (m *Middleware) Auth(guardNames ...string) gin.HandlerFunc {
	guards := parseGuards(guardNames)

	return func(c *gin.Context) {
		raw, err := bearerToken(c)
		if err != nil {
			abortWithError(c, err)
			return
		}

		var reqCtx context.Context
		if m.authManager != nil {
			reqCtx, err = m.authenticateWithManager(c.Request.Context(), raw, guards)
		} else {
			reqCtx, err = m.authenticateFallback(c.Request.Context(), raw, guards)
		}

		if err != nil {
			abortWithError(c, err)
			return
		}

		if m.authz != nil {
			reqCtx = authentication.WithGate(reqCtx, m.authz)
		}
		c.Request = c.Request.WithContext(reqCtx)

		enrichSentryUser(c, reqCtx)

		c.Next()
	}
}

// enrichSentryUser 将已认证用户身份与角色元信息同步绑定至 Sentry / GlitchTip 请求级作用域。
func enrichSentryUser(c *gin.Context, reqCtx context.Context) {
	id, ok := authentication.FromContext(reqCtx)
	if !ok {
		return
	}

	var email string
	if id.Claims != nil {
		if emailVal, found := id.Claims.GetClaim("email"); found {
			email, _ = emailVal.(string)
		}
	}

	platformsentry.SetUser(reqCtx, id.UserID, email, id.Role, c.ClientIP())
	platformsentry.SetTag(reqCtx, "role", id.Role)
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

// authenticateFallback 当未注入 AuthManager 时提供向下兼容的基础令牌校验。
func (m *Middleware) authenticateFallback(ctx context.Context, raw string, guards []string) (context.Context, error) {
	if len(guards) > 0 {
		return nil, errs.Internal(errors.New("middleware: auth manager required for named guard"))
	}
	if m.tokens == nil {
		return nil, errs.Internal(errors.New("middleware: token issuer not configured"))
	}
	newCtx, _, err := m.tokens.AuthenticateToken(ctx, raw)
	return newCtx, err
}

// Can 【路由级中间件】仿照 Laravel can 中间件设计，基于令牌角色执行 Casbin RBAC 细粒度权限校验。
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
	var guards []string
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
