package authentication

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"bgin/internal/platform/errs"
)

// Identity 描述已认证请求的上下文身份凭据与会话元数据。
type Identity struct {
	UserID         string
	Role           string
	Roles          []string
	TokenID        string
	TokenExpiresAt time.Time
	Claims         *Claims
}

// AllRoles 返回当前身份的全部角色列表（兼容单个 Role 字段）。
// 采用防御性拷贝隔离底层数组，杜绝调用方意外或恶意篡改身份状态。
func (id Identity) AllRoles() []string {
	if len(id.Roles) > 0 {
		return slices.Clone(id.Roles)
	}
	if id.Role != "" {
		return []string{id.Role}
	}
	return nil
}

// String 实现 fmt.Stringer，格式化关键元数据。
func (id Identity) String() string {
	return fmt.Sprintf("Identity{UserID:%q, Role:%q, TokenID:%q}", id.UserID, id.Role, id.TokenID)
}

// GoString 实现 fmt.GoStringer。
func (id Identity) GoString() string {
	return id.String()
}

// ctxKey 是 context 中存储认证信息的私有键类型。
type ctxKey struct{}

// gateCtxKey 是 context 中存储 Gate 授权门禁的私有键类型。
type gateCtxKey struct{}

// requestAuthCache 承载请求生命周期内的用户实体缓存与单次加载控制。
type requestAuthCache struct {
	identity Identity
	hasIdent bool
	user     any
	userErr  error
	userOnce sync.Once
}

// With 将已解析的身份凭据注入请求上下文，并初始化请求级缓存。
func With(ctx context.Context, id Identity) context.Context {
	if len(id.Roles) == 0 && id.Role != "" {
		id.Roles = []string{id.Role}
	}
	cache := &requestAuthCache{
		identity: id,
		hasIdent: true,
	}
	return context.WithValue(ctx, ctxKey{}, cache)
}

// WithUser 将已明确加载的用户实体预置至请求上下文，并同步设置身份信息。
// 使当前及后续请求处理流程中无需再执行数据库查询。
func WithUser(ctx context.Context, user Authenticatable) context.Context {
	if user == nil {
		return ctx
	}
	cache, ok := ctx.Value(ctxKey{}).(*requestAuthCache)
	if !ok || cache == nil {
		cache = &requestAuthCache{}
		ctx = context.WithValue(ctx, ctxKey{}, cache)
	}
	cache.user = user
	cache.identity.UserID = user.GetAuthIdentifier()
	if cache.identity.Role == "" {
		cache.identity.Role = user.GetAuthRole()
	}
	if len(cache.identity.Roles) == 0 {
		if roled, ok := user.(RoledSubject); ok {
			cache.identity.Roles = roled.GetAuthRoles()
		} else if cache.identity.Role != "" {
			cache.identity.Roles = []string{cache.identity.Role}
		}
	}
	cache.hasIdent = true
	return ctx
}

// FromContext 从上下文中提取身份凭据；未认证时返回 false。
func FromContext(ctx context.Context) (Identity, bool) {
	cache, ok := ctx.Value(ctxKey{}).(*requestAuthCache)
	if !ok || cache == nil || !cache.hasIdent {
		return Identity{}, false
	}
	return cache.identity, true
}

// UserFromContext 从上下文中高速提取已缓存的用户实体；未预置或类型不符时返回 false。
func UserFromContext[T Authenticatable](ctx context.Context) (T, bool) {
	var zero T
	cache, ok := ctx.Value(ctxKey{}).(*requestAuthCache)
	if !ok || cache == nil || cache.user == nil {
		return zero, false
	}
	u, ok := cache.user.(T)
	return u, ok
}

// WithGate 将 Gate 授权门禁注入请求上下文。
func WithGate(ctx context.Context, gate Gate) context.Context {
	if gate == nil {
		return ctx
	}
	return context.WithValue(ctx, gateCtxKey{}, gate)
}

// GateFromContext 从上下文中提取 Gate 授权门禁；未注入时返回 nil, false。
func GateFromContext(ctx context.Context) (Gate, bool) {
	g, ok := ctx.Value(gateCtxKey{}).(Gate)
	return g, ok && g != nil
}

// Can 是上下文级全局鉴权门面便捷方法。
// 优先委托给上下文注入的 Gate；若无 Gate 则返回 false。
func Can(ctx context.Context, resource, action string) bool {
	if gate, ok := GateFromContext(ctx); ok {
		return gate.Can(ctx, resource, action)
	}
	return false
}

// Authorize 是上下文级全局鉴权门面拦截方法。
// 若无权限直接返回统一的 403 Forbidden 业务错误。
func Authorize(ctx context.Context, resource, action string) error {
	if !Can(ctx, resource, action) {
		return errs.Forbidden("You are not allowed to perform this action")
	}
	return nil
}
