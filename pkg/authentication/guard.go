package authentication

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"reflect"
	"strings"
	"time"

	"cgin/pkg/errs"
)

// 守卫常用错误定义。
var (
	// ErrProviderNotConfigured 表示 Guard 尚未配置用户仓储提供者。
	ErrProviderNotConfigured = errors.New("authentication: user provider is not configured")
	// ErrCachedUserTypeMismatch 表示上下文缓存的用户实体类型不符。
	ErrCachedUserTypeMismatch = errors.New("authentication: cached user type mismatch")
)

// Guard 是核心认证守卫泛型结构体，全面实现类似 Laravel / jwt-auth 的方法契约。
type Guard[T any] struct {
	tokens   *TokenIssuer
	provider UserProvider[T]
	policy   Policy[T]
	identity func(T) Identity
}

var _ StatefulGuard = (*Guard[any])(nil)

// NewGuard 构造通用泛型 Guard。
//
// identity 是平台与业务实体之间唯一的桥：平台只依赖它投影出的 Identity
// （主体 ID、角色与自定义声明），不感知任何数据库行类型。
func NewGuard[T any](tokens *TokenIssuer, provider UserProvider[T], policy Policy[T], identity func(T) Identity) *Guard[T] {
	if policy == nil {
		policy = AllowAllPolicy[T]{}
	}
	return &Guard[T]{
		tokens:   tokens,
		provider: provider,
		policy:   policy,
		identity: identity,
	}
}

// identify 投影主体身份；未提供投影函数时返回零值身份。
func (g *Guard[T]) identify(user T) Identity {
	if g.identity == nil {
		return Identity{}
	}
	return g.identity(user)
}

// Provider 返回底层 UserProvider。
func (g *Guard[T]) Provider() UserProvider[T] {
	return g.provider
}

// Policy 返回底层 Policy。
func (g *Guard[T]) Policy() Policy[T] {
	return g.policy
}

// Check 判断当前请求上下文是否包含已认证的用户身份。
func (g *Guard[T]) Check(ctx context.Context) bool {
	id, ok := FromContext(ctx)
	return ok && id.UserID != ""
}

// Guest 判断当前请求上下文是否为访客（未认证）。
func (g *Guard[T]) Guest(ctx context.Context) bool {
	return !g.Check(ctx)
}

// HasUser 判断当前请求上下文中是否已预置或已缓存用户实体（0 次数据库查询）。
func (g *Guard[T]) HasUser(ctx context.Context) bool {
	_, ok := UserFromContext[T](ctx)
	return ok
}

// SetUser 将指定实体显式绑定至当前请求上下文（对应 Laravel/jwt-auth 的 setUser）。
func (g *Guard[T]) SetUser(ctx context.Context, user T) context.Context {
	return WithUser(ctx, user, g.identify(user))
}

// ID 从上下文中高速提取当前已认证用户的唯一标识（0 次数据库开销）。未认证时返回 401 错误。
func (g *Guard[T]) ID(ctx context.Context) (string, error) {
	id, ok := FromContext(ctx)
	if !ok || id.UserID == "" {
		return "", errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	return id.UserID, nil
}

// User 惰性加载并缓存当前已认证用户实体（单请求生命周期内后续调用 0 次数据库查询）。未登录时返回 (zero, nil)。
func (g *Guard[T]) User(ctx context.Context) (T, error) {
	var zero T
	cache, ok := ctx.Value(ctxKey{}).(*requestAuthCache)
	if !ok || cache == nil || !cache.hasIdent || cache.identity.UserID == "" {
		return zero, nil
	}

	cache.userOnce.Do(func() {
		if cache.user != nil {
			return
		}
		if g.provider == nil {
			cache.userErr = ErrProviderNotConfigured
			return
		}
		user, err := g.provider.FindByID(ctx, cache.identity.UserID)
		if err != nil {
			if isUserNotFound(err) {
				cache.userErr = errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
				return
			}
			cache.userErr = errs.Internal(err)
			return
		}
		if g.policy != nil {
			if err := g.policy.ValidateUser(ctx, user); err != nil {
				cache.userErr = err
				return
			}
		}
		cache.user = user
	})

	if cache.userErr != nil {
		return zero, cache.userErr
	}
	if cache.user == nil {
		return zero, nil
	}
	u, ok := cache.user.(T)
	if !ok {
		return zero, ErrCachedUserTypeMismatch
	}
	return u, nil
}

// UserOrFail 获取当前已认证用户实体；未登录或用户不存在时严格返回 401 错误。
func (g *Guard[T]) UserOrFail(ctx context.Context) (T, error) {
	var zero T
	if !g.Check(ctx) {
		return zero, errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	user, err := g.User(ctx)
	if err != nil {
		return zero, err
	}
	if isNilOrZero(user) {
		return zero, errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	return user, nil
}

// ResolveUser 优先从当前认证上下文提取用户实体；若上下文尚未绑定身份，则按 userID 回源加载并执行守卫策略校验。
func (g *Guard[T]) ResolveUser(ctx context.Context, userID string) (T, error) {
	var zero T
	if g.Check(ctx) {
		return g.UserOrFail(ctx)
	}
	if strings.TrimSpace(userID) == "" {
		return zero, errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	if g.provider == nil {
		return zero, errs.Internal(ErrProviderNotConfigured)
	}
	user, err := g.provider.FindByID(ctx, userID)
	if err != nil {
		if isUserNotFound(err) {
			return zero, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
		}
		return zero, errs.Internal(err)
	}
	if g.policy != nil {
		if err := g.policy.ValidateUser(ctx, user); err != nil {
			return zero, err
		}
	}
	return user, nil
}

// isUserNotFound 判断错误是否代表用户记录不存在（兼容平台哨兵错误与数据库空行错误）。
func isUserNotFound(err error) bool {
	return errors.Is(err, ErrUserNotFound) ||
		errors.Is(err, sql.ErrNoRows) ||
		(err != nil && strings.HasSuffix(err.Error(), "no rows in result set"))
}

func isNilOrZero[T any](v T) bool {
	if any(v) == nil {
		return true
	}
	val := reflect.ValueOf(v)
	switch val.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return val.IsNil()
	default:
		var zero T
		return reflect.DeepEqual(v, zero)
	}
}

// Validate 仅校验用户账号与凭据是否有效及守卫策略，不签发令牌。校验成功返回已验证的实体。
func (g *Guard[T]) Validate(ctx context.Context, creds Credentials) (T, error) {
	var zero T
	if strings.TrimSpace(creds.Account) == "" || creds.Password == "" {
		return zero, errs.InvalidCredentials()
	}
	if g.provider == nil {
		return zero, ErrProviderNotConfigured
	}

	user, err := g.provider.FindByCredentials(ctx, creds)
	if err != nil {
		if isUserNotFound(err) {
			if equaliser, ok := any(g.provider).(TimingEqualiser); ok && equaliser != nil {
				equaliser.EqualiseTiming(ctx, creds.Password)
			}
			return zero, errs.InvalidCredentials()
		}
		return zero, errs.Internal(err)
	}

	matched, err := g.provider.ValidateCredentials(ctx, user, creds)
	if err != nil {
		return zero, errs.Internal(err)
	}
	if !matched {
		return zero, errs.InvalidCredentials()
	}

	if g.policy != nil {
		if err := g.policy.ValidateUser(ctx, user); err != nil {
			return zero, err
		}
	}

	return user, nil
}

// Attempt 尝试基于凭据认证并登录用户，成功后签发双令牌并自动记录登录审计。
func (g *Guard[T]) Attempt(ctx context.Context, creds Credentials, opts ...Option) (T, Session, error) {
	var zero T
	user, err := g.Validate(ctx, creds)
	if err != nil {
		return zero, Session{}, err
	}

	sess, err := g.Login(ctx, user, opts...)
	if err != nil {
		return zero, Session{}, err
	}

	if recorder, ok := any(g.provider).(LoginRecorder); ok && recorder != nil {
		_ = recorder.RecordLogin(ctx, g.identify(user).UserID, "credential")
	}
	return user, sess, nil
}

// Login 为指定用户实体签发会话凭据。若实体实现了 JWTSubject 接口，则自动提取并合并其自定义声明。
func (g *Guard[T]) Login(ctx context.Context, user T, opts ...Option) (Session, error) {
	if g.policy != nil {
		if err := g.policy.ValidateUser(ctx, user); err != nil {
			return Session{}, err
		}
	}

	return g.issueSession(user, opts...)
}

// issueSession 提取用户自定义声明并签发会话令牌对。
func (g *Guard[T]) issueSession(user T, opts ...Option) (Session, error) {
	cfg := g.resolveOptions(opts...)
	identity := g.identify(user)

	// 身份投影中的自定义声明对应 jwt-auth 的 getJWTCustomClaims。
	if len(identity.CustomClaims) > 0 {
		if cfg.claims == nil {
			cfg.claims = make(map[string]any, len(identity.CustomClaims))
		}
		maps.Copy(cfg.claims, identity.CustomClaims)
	}

	pair, err := g.tokens.IssueWithCustom(identity.UserID, identity.Role, cfg.ttl, 0, cfg.claims, cfg.withRefreshToken)
	if err != nil {
		return Session{}, errs.Internal(err)
	}

	return Session{
		Token:        pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    int(time.Until(pair.AccessExpiresAt).Seconds()),
	}, nil
}

// LoginUsingID 按用户 ID 查询用户并完成登录与令牌签发。
func (g *Guard[T]) LoginUsingID(ctx context.Context, userID string, opts ...Option) (T, Session, error) {
	var zero T
	if g.provider == nil {
		return zero, Session{}, ErrProviderNotConfigured
	}
	user, err := g.provider.FindByID(ctx, userID)
	if err != nil {
		if isUserNotFound(err) {
			return zero, Session{}, errs.NotFound("The requested user does not exist")
		}
		return zero, Session{}, errs.Internal(err)
	}
	sess, err := g.Login(ctx, user, opts...)
	if err != nil {
		return zero, Session{}, err
	}
	return user, sess, nil
}

// Logout 作废当前请求上下文关联的访问令牌；若传入了刷新令牌则一并撤销。
func (g *Guard[T]) Logout(ctx context.Context, refreshToken ...string) error {
	id, ok := FromContext(ctx)
	if !ok {
		return nil
	}

	// 1. 作废访问令牌
	if err := g.tokens.RevokeByID(ctx, id.TokenID, id.TokenExpiresAt); err != nil {
		return errs.Internal(err)
	}

	// 2. 若传入刷新令牌，做归属比对并作废
	if len(refreshToken) > 0 && strings.TrimSpace(refreshToken[0]) != "" {
		claims, err := g.tokens.Parse(strings.TrimSpace(refreshToken[0]), TypeRefresh)
		if err == nil && claims != nil && claims.Subject == id.UserID {
			_ = g.tokens.Revoke(ctx, claims)
		}
	}

	return nil
}

// Refresh 解析刷新令牌、校验防重放黑名单、验证用户守卫策略、作废旧刷新令牌并签发新双令牌。
func (g *Guard[T]) Refresh(ctx context.Context, refreshToken string, opts ...Option) (T, Session, error) {
	var zero T
	claims, err := g.tokens.Parse(strings.TrimSpace(refreshToken), TypeRefresh)
	if err != nil {
		return zero, Session{}, err
	}

	revoked, err := g.tokens.IsTokenRevoked(ctx, claims)
	if err != nil {
		return zero, Session{}, errs.Internal(err)
	}
	if revoked {
		// 防令牌重放与窃取侦测 (OWASP Token Reuse Detection)：
		// 当侦测到已撤销的刷新令牌再次被尝试用于刷新时，判定存在令牌泄露或窃取重放攻击，
		// 立即触发该用户全设备凭据注销 (InvalidateAll)，并阻断请求。
		_ = g.InvalidateAll(ctx, claims.Subject)
		return zero, Session{}, errs.Unauthorized(errs.CodeAuthTokenRevoked, "The token has been revoked")
	}

	if g.provider == nil {
		return zero, Session{}, ErrProviderNotConfigured
	}
	user, err := g.provider.FindByID(ctx, claims.Subject)
	if err != nil {
		if isUserNotFound(err) {
			return zero, Session{}, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
		}
		return zero, Session{}, errs.Internal(err)
	}

	if g.policy != nil {
		if err := g.policy.ValidateUser(ctx, user); err != nil {
			return zero, Session{}, err
		}
	}

	if err := g.tokens.Revoke(ctx, claims); err != nil {
		return zero, Session{}, errs.Internal(err)
	}

	sess, err := g.Login(ctx, user, opts...)
	if err != nil {
		return zero, Session{}, err
	}

	return user, sess, nil
}

// Invalidate 根据令牌 ID 与有效期直接将其加入黑名单。
func (g *Guard[T]) Invalidate(ctx context.Context, tokenID string, expiresAt time.Time) error {
	if err := g.tokens.RevokeByID(ctx, tokenID, expiresAt); err != nil {
		return errs.Internal(err)
	}
	return nil
}

// InvalidateToken 作废当前请求上下文中的访问令牌（对应 jwt-auth 的 auth()->invalidate()）。
func (g *Guard[T]) InvalidateToken(ctx context.Context) error {
	id, ok := FromContext(ctx)
	if !ok || id.TokenID == "" {
		return nil
	}
	return g.Invalidate(ctx, id.TokenID, id.TokenExpiresAt)
}

// InvalidateAll 触发用户级注销纪元，在 O(1) 复杂度内作废该用户在此之前签发的所有访问令牌与刷新令牌。
func (g *Guard[T]) InvalidateAll(ctx context.Context, userID string) error {
	if err := g.tokens.RevokeUser(ctx, userID); err != nil {
		return errs.Internal(err)
	}
	return nil
}

// LogoutAll 作废当前已认证用户在所有设备上的会话凭据。
func (g *Guard[T]) LogoutAll(ctx context.Context) error {
	id, ok := FromContext(ctx)
	if !ok || id.UserID == "" {
		return nil
	}
	return g.InvalidateAll(ctx, id.UserID)
}

// AuthenticateToken 校验并解析原始令牌，在黑名单检查与策略通过后注入请求上下文（对应 jwt-auth 的 setToken）。
func (g *Guard[T]) AuthenticateToken(ctx context.Context, rawToken string) (context.Context, *Claims, error) {
	newCtx, claims, err := g.tokens.AuthenticateToken(ctx, rawToken)
	if err != nil {
		return ctx, nil, err
	}

	// 守卫策略校验：若配置了用户仓储与校验策略，则对用户实体执行状态与角色前置策略校验
	if g.provider != nil && g.policy != nil {
		user, err := g.User(newCtx)
		if err != nil {
			return ctx, nil, err
		}
		if isNilOrZero(user) {
			return ctx, nil, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
		}
		newCtx = g.SetUser(newCtx, user)
	}

	return newCtx, claims, nil
}

// SetToken 显式注入并认证原始令牌（对应 jwt-auth 的 setToken）。
func (g *Guard[T]) SetToken(ctx context.Context, rawToken string) (context.Context, error) {
	newCtx, _, err := g.AuthenticateToken(ctx, rawToken)
	return newCtx, err
}

// Claims 从当前上下文中获取强类型的令牌载荷。
func (g *Guard[T]) Claims(ctx context.Context) (*Claims, error) {
	id, ok := FromContext(ctx)
	if !ok || id.Claims == nil {
		return nil, errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	return id.Claims, nil
}

// GetClaim 提取当前令牌载荷中的指定键值。
func (g *Guard[T]) GetClaim(ctx context.Context, key string) (any, error) {
	claims, err := g.Claims(ctx)
	if err != nil {
		return nil, err
	}
	val, ok := claims.GetClaim(key)
	if !ok {
		return nil, errs.NotFound("Claim not found: " + key)
	}
	return val, nil
}

// Payload 获取当前令牌的全部载荷字典。
func (g *Guard[T]) Payload(ctx context.Context) (map[string]any, error) {
	claims, err := g.Claims(ctx)
	if err != nil {
		return nil, err
	}
	return claims.Payload(), nil
}

// resolveOptions 解析合并默认值与运行时 Functional Options。
func (g *Guard[T]) resolveOptions(opts ...Option) *options {
	res := defaultOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(res)
		}
	}
	return res
}
