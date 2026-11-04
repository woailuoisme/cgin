package authentication

import (
	"context"
	"slices"

	"cgin/pkg/errs"
)

type (
	ctxKey      struct{}
	tokenCtxKey struct{}
	userCtxKey  struct{}
)

// JWTSubject 声明用户模型可选实现的自定义 JWT 声明提供契约。
type JWTSubject interface {
	GetJWTCustomClaims() map[string]any
}

// Identity 表示经守卫认证后的请求主体身份快照。
type Identity struct {
	UserID       string
	Role         string
	Roles        []string
	CustomClaims map[string]any
	Claims       *Claims
}

// HasRole 判断主体是否拥有指定角色。
func (id Identity) HasRole(role string) bool {
	if role == "" {
		return false
	}
	return id.Role == role || slices.Contains(id.Roles, role)
}

// HasAnyRole 判断主体是否拥有候选角色中的任意一个。
func (id Identity) HasAnyRole(roles ...string) bool {
	return slices.ContainsFunc(roles, id.HasRole)
}

// AllRoles 返回去重后的完整角色列表。
func (id Identity) AllRoles() []string {
	if len(id.Roles) == 0 {
		if id.Role != "" {
			return []string{id.Role}
		}
		return nil
	}
	out := make([]string, 0, len(id.Roles)+1)
	if id.Role != "" {
		out = append(out, id.Role)
	}
	for _, r := range id.Roles {
		if r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// WithIdentity 将认证身份写入上下文。
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// With 是 WithIdentity 的简写别名。
func With(ctx context.Context, id Identity) context.Context {
	return WithIdentity(ctx, id)
}

// FromContext 从上下文读取已认证身份；未认证时返回 false。
func FromContext(ctx context.Context) (Identity, bool) {
	if ctx == nil {
		return Identity{}, false
	}
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok && id.UserID != ""
}

// RequireIdentity 从上下文读取认证身份；缺失时返回统一 401 业务错误。
func RequireIdentity(ctx context.Context) (Identity, error) {
	id, ok := FromContext(ctx)
	if !ok {
		return Identity{}, errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	return id, nil
}

// WithToken 将原始 Bearer 访问令牌写入上下文，供登出吊销使用。
func WithToken(ctx context.Context, rawToken string) context.Context {
	return context.WithValue(ctx, tokenCtxKey{}, rawToken)
}

// TokenFromContext 从上下文中提取原始 Bearer 访问令牌。
func TokenFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	tok, ok := ctx.Value(tokenCtxKey{}).(string)
	return tok, ok && tok != ""
}

// WithUser 将已加载的用户实体与身份一并缓存在请求上下文中，避免单次请求内重复查库。
func WithUser[T any](ctx context.Context, user T, id Identity) context.Context {
	ctx = WithIdentity(ctx, id)
	return context.WithValue(ctx, userCtxKey{}, user)
}

// UserFromContext 从上下文中提取已缓存的用户实体。
func UserFromContext[T any](ctx context.Context) (T, bool) {
	var zero T
	if ctx == nil {
		return zero, false
	}
	u, ok := ctx.Value(userCtxKey{}).(T)
	return u, ok
}
