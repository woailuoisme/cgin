package authentication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/samber/oops"

	"cgin/pkg/errs"
)

var (
	// ErrUnauthenticated 表示当前上下文未认证或令牌无效。
	ErrUnauthenticated = errors.New("authentication: unauthenticated")
	// ErrInvalidCredentials 表示账号不存在或密码错误。
	ErrInvalidCredentials = errors.New("authentication: invalid credentials")
	// ErrAccountDisabled 表示账号未激活或已封禁。
	ErrAccountDisabled = errors.New("authentication: account disabled or banned")
	// ErrForbidden 表示认证主体无权访问目标守卫域。
	ErrForbidden = errors.New("authentication: forbidden")
	// ErrUserNotFound 表示底层存储未找到对应用户。
	ErrUserNotFound = errors.New("authentication: user not found")
	// ErrGuardNotFound 表示未找到指定名称的守卫。
	ErrGuardNotFound = errors.New("authentication: guard not found")
)

// Session 表示一次登录或续签产出的双令牌会话。
type Session struct {
	Token        string    `json:"token"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresIn    int       `json:"expiresIn"`
	IssuedAt     time.Time `json:"issuedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// Credentials 封装账号密码登录凭据。
type Credentials struct {
	Account  string
	Password string
	Extra    map[string]any
}

// UserProvider 定义守卫回源加载用户与校验凭据的最小契约。
type UserProvider[T any] interface {
	FindByID(ctx context.Context, id string) (T, error)
	FindByCredentials(ctx context.Context, creds Credentials) (T, error)
	ValidateCredentials(ctx context.Context, user T, creds Credentials) (bool, error)
}

// LoginRecorder 供 UserProvider 可选实现，以记录成功登录审计信息。
type LoginRecorder interface {
	RecordLogin(ctx context.Context, id string, method string) error
}

// TimingEqualiser 供 UserProvider 可选实现，在账号不存在时执行等价耗时哈希运算以阻断时序枚举。
type TimingEqualiser interface {
	EqualiseTiming(ctx context.Context, secret string)
}

// Policy 定义守卫对用户状态与角色准入的策略校验契约。
type Policy[T any] interface {
	ValidateUser(ctx context.Context, user T) error
}

// StatefulGuard 定义传输层中间件解析 Bearer 令牌所需的协议无关守卫契约。
type StatefulGuard interface {
	Authenticate(ctx context.Context, rawToken string) (context.Context, error)
}

type loginConfig struct {
	accessTTL        time.Duration
	refreshTTL       time.Duration
	customClaims     map[string]any
	withRefreshToken bool
}

// LoginOption 定制单次登录或令牌刷新的签发参数。
type LoginOption func(*loginConfig)

// WithTTL 覆盖本次签发的访问令牌有效期。
func WithTTL(ttl time.Duration) LoginOption {
	return func(c *loginConfig) { c.accessTTL = ttl }
}

// WithRefreshTTL 覆盖本次签发的刷新令牌有效期。
func WithRefreshTTL(ttl time.Duration) LoginOption {
	return func(c *loginConfig) { c.refreshTTL = ttl }
}

// WithClaims 向本次签发的令牌注入额外自定义声明。
func WithClaims(claims map[string]any) LoginOption {
	return func(c *loginConfig) {
		if c.customClaims == nil {
			c.customClaims = make(map[string]any, len(claims))
		}
		maps.Copy(c.customClaims, claims)
	}
}

// WithRefreshToken 控制本次签发是否同时生成刷新令牌（默认开启）。
func WithRefreshToken(enabled bool) LoginOption {
	return func(c *loginConfig) { c.withRefreshToken = enabled }
}

// Guard 是强类型认证守卫，协调 JWT 签发验签、用户回源加载与准入策略校验。
type Guard[T comparable] struct {
	tokens   *TokenIssuer
	provider UserProvider[T]
	policy   Policy[T]
	identity func(T) Identity
}

// NewGuard 构造认证守卫实例。
func NewGuard[T comparable](
	tokens *TokenIssuer,
	provider UserProvider[T],
	policy Policy[T],
	identity func(T) Identity,
) *Guard[T] {
	return &Guard[T]{
		tokens:   tokens,
		provider: provider,
		policy:   policy,
		identity: identity,
	}
}

// Check 判断当前上下文是否已通过认证。
func (g *Guard[T]) Check(ctx context.Context) bool {
	_, ok := FromContext(ctx)
	return ok
}

// ID 从上下文提取已认证用户的唯一主键。
func (g *Guard[T]) ID(ctx context.Context) (string, error) {
	id, err := RequireIdentity(ctx)
	if err != nil {
		return "", err
	}
	return id.UserID, nil
}

// User 获取当前已认证用户实体；未认证时返回零值与 nil 错误。
func (g *Guard[T]) User(ctx context.Context) (T, error) {
	var zero T
	if u, ok := UserFromContext[T](ctx); ok && u != zero {
		return u, nil
	}
	id, ok := FromContext(ctx)
	if !ok {
		return zero, nil
	}
	if g.provider == nil {
		return zero, errs.Internal(errors.New("authentication: user provider is not configured"))
	}
	return g.loadAndValidate(ctx, id.UserID)
}

// UserOrFail 获取当前已认证用户实体；未认证或用户无效时返回业务错误。
func (g *Guard[T]) UserOrFail(ctx context.Context) (T, error) {
	var zero T
	if !g.Check(ctx) {
		return zero, errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	u, err := g.User(ctx)
	if err != nil {
		return zero, err
	}
	if u == zero {
		return zero, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The user associated with this token no longer exists")
	}
	return u, nil
}

// ResolveUser 优先从请求上下文读取已缓存用户，未命中时按 userID 回源加载并执行策略校验。
func (g *Guard[T]) ResolveUser(ctx context.Context, userID string) (T, error) {
	var zero T
	if u, ok := UserFromContext[T](ctx); ok && u != zero {
		if g.identity == nil || g.identity(u).UserID == userID {
			if err := g.validatePolicy(ctx, u); err != nil {
				return zero, err
			}
			return u, nil
		}
	}
	if g.provider == nil {
		return zero, errs.Internal(errors.New("authentication: user provider is not configured"))
	}
	return g.loadAndValidate(ctx, userID)
}

// SetUser 将用户实体及其身份注入上下文。
func (g *Guard[T]) SetUser(ctx context.Context, user T) context.Context {
	var id Identity
	if g.identity != nil {
		id = g.identity(user)
	}
	return WithUser(ctx, user, id)
}

// Authenticate 实现 StatefulGuard 接口，供传输层中间件完成 Bearer 令牌认证。
func (g *Guard[T]) Authenticate(ctx context.Context, rawToken string) (context.Context, error) {
	outCtx, _, err := g.AuthenticateToken(ctx, rawToken)
	return outCtx, err
}

// AuthenticateToken 解析访问令牌、检查黑名单并执行用户准入策略，将用户与身份写入返回上下文。
func (g *Guard[T]) AuthenticateToken(ctx context.Context, rawToken string) (context.Context, *Claims, error) {
	if g.tokens == nil {
		return ctx, nil, errs.Internal(errors.New("authentication: token issuer is not configured"))
	}

	authCtx, claims, err := g.tokens.AuthenticateToken(ctx, rawToken)
	if err != nil {
		return ctx, nil, err
	}

	if g.provider == nil {
		return authCtx, claims, nil
	}

	u, err := g.loadAndValidate(authCtx, claims.Subject)
	if err != nil {
		return ctx, nil, err
	}
	id := Identity{UserID: claims.Subject, Role: claims.Role}
	if g.identity != nil {
		id = g.identity(u)
	}
	id.Claims = claims
	if len(claims.CustomClaims) > 0 {
		if id.CustomClaims == nil {
			id.CustomClaims = make(map[string]any, len(claims.CustomClaims))
		}
		maps.Copy(id.CustomClaims, claims.CustomClaims)
	}
	return WithUser(authCtx, u, id), claims, nil
}

// Login 校验用户策略并签发会话令牌。
func (g *Guard[T]) Login(ctx context.Context, user T, opts ...LoginOption) (Session, error) {
	var zero T
	if user == zero {
		return Session{}, errs.Unauthorized(errs.CodeAuthTokenInvalid, "Cannot login with nil user")
	}
	if err := g.validatePolicy(ctx, user); err != nil {
		return Session{}, err
	}
	if g.identity == nil {
		return Session{}, errs.Internal(errors.New("authentication: identity extractor is not configured"))
	}

	id := g.identity(user)
	if id.UserID == "" {
		return Session{}, errs.Internal(errors.New("authentication: user identity has empty UserID"))
	}

	cfg := &loginConfig{withRefreshToken: true}
	if len(id.CustomClaims) > 0 {
		cfg.customClaims = maps.Clone(id.CustomClaims)
	}
	if subject, ok := any(user).(JWTSubject); ok {
		if extra := subject.GetJWTCustomClaims(); len(extra) > 0 {
			if cfg.customClaims == nil {
				cfg.customClaims = make(map[string]any, len(extra))
			}
			maps.Copy(cfg.customClaims, extra)
		}
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	pair, err := g.tokens.IssueWithCustom(
		id.UserID, id.Role,
		cfg.accessTTL, cfg.refreshTTL,
		cfg.customClaims, cfg.withRefreshToken,
	)
	if err != nil {
		return Session{}, err
	}

	now := time.Now().UTC()
	return Session{
		Token:        pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    max(int(pair.AccessExpiresAt.Sub(now).Seconds()), 0),
		IssuedAt:     now,
		ExpiresAt:    pair.AccessExpiresAt,
	}, nil
}

// Validate 校验登录凭据与账号状态策略，但不签发令牌。
func (g *Guard[T]) Validate(ctx context.Context, creds Credentials) (T, error) {
	var zero T
	if g.provider == nil {
		return zero, errs.Internal(errors.New("authentication: user provider is not configured"))
	}

	user, err := g.provider.FindByCredentials(ctx, creds)
	if err != nil {
		if isNotFoundErr(err) {
			if eq, ok := g.provider.(TimingEqualiser); ok {
				eq.EqualiseTiming(ctx, creds.Password)
			}
			return zero, errs.InvalidCredentials()
		}
		return zero, oops.In("authentication").Wrapf(err, "find user by credentials")
	}
	if user == zero {
		if eq, ok := g.provider.(TimingEqualiser); ok {
			eq.EqualiseTiming(ctx, creds.Password)
		}
		return zero, errs.InvalidCredentials()
	}

	ok, err := g.provider.ValidateCredentials(ctx, user, creds)
	if err != nil {
		return zero, oops.In("authentication").Wrapf(err, "validate credentials")
	}
	if !ok {
		return zero, errs.InvalidCredentials()
	}

	if err := g.validatePolicy(ctx, user); err != nil {
		return zero, err
	}
	return user, nil
}

// Attempt 校验凭据、签发双令牌会话并触发登录审计记录。
func (g *Guard[T]) Attempt(ctx context.Context, creds Credentials, opts ...LoginOption) (T, Session, error) {
	var zero T
	user, err := g.Validate(ctx, creds)
	if err != nil {
		return zero, Session{}, err
	}

	sess, err := g.Login(ctx, user, opts...)
	if err != nil {
		return zero, Session{}, err
	}

	if recorder, ok := g.provider.(LoginRecorder); ok && g.identity != nil {
		_ = recorder.RecordLogin(ctx, g.identity(user).UserID, "credential")
	}
	return user, sess, nil
}

// Refresh 校验并作废旧刷新令牌（单次轮换），回源校验用户最新状态后签发新双令牌。
func (g *Guard[T]) Refresh(ctx context.Context, refreshToken string, opts ...LoginOption) (T, Session, error) {
	var zero T
	claims, err := g.tokens.Parse(refreshToken, TypeRefresh)
	if err != nil {
		return zero, Session{}, err
	}

	revoked, err := g.tokens.IsTokenRevoked(ctx, claims)
	if err != nil {
		return zero, Session{}, oops.In("authentication").Wrapf(err, "check refresh token revocation")
	}
	if revoked {
		return zero, Session{}, errs.Unauthorized(errs.CodeAuthTokenRevoked, "The refresh token has already been used or revoked")
	}

	if err := g.tokens.Revoke(ctx, claims); err != nil {
		return zero, Session{}, oops.In("authentication").Wrapf(err, "revoke old refresh token")
	}

	if g.provider == nil {
		return zero, Session{}, errs.Internal(errors.New("authentication: user provider is not configured"))
	}

	user, err := g.loadAndValidate(ctx, claims.Subject)
	if err != nil {
		return zero, Session{}, err
	}

	sess, err := g.Login(ctx, user, opts...)
	if err != nil {
		return zero, Session{}, err
	}
	return user, sess, nil
}

// Logout 作废当前上下文中的访问令牌以及可选传入的刷新令牌。
func (g *Guard[T]) Logout(ctx context.Context, refreshToken ...string) error {
	if rawAccess, ok := TokenFromContext(ctx); ok && rawAccess != "" {
		if claims, err := g.tokens.Parse(rawAccess, TypeAccess); err == nil {
			if err := g.tokens.Revoke(ctx, claims); err != nil {
				return err
			}
		}
	}
	for _, rt := range refreshToken {
		if rt == "" {
			continue
		}
		if claims, err := g.tokens.Parse(rt, TypeRefresh); err == nil {
			if err := g.tokens.Revoke(ctx, claims); err != nil {
				return err
			}
		}
	}
	return nil
}

// InvalidateAll 刷新指定用户的注销纪元，使其此前签发的所有令牌立即失效。
func (g *Guard[T]) InvalidateAll(ctx context.Context, userID string) error {
	return g.tokens.RevokeUser(ctx, userID)
}

func (g *Guard[T]) loadAndValidate(ctx context.Context, userID string) (T, error) {
	var zero T
	u, err := g.provider.FindByID(ctx, userID)
	if err != nil {
		if isNotFoundErr(err) {
			return zero, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
		}
		return zero, oops.In("authentication").With("user_id", userID).Wrapf(err, "find user by id")
	}
	if u == zero {
		return zero, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
	}
	if err := g.validatePolicy(ctx, u); err != nil {
		return zero, err
	}
	return u, nil
}

func (g *Guard[T]) validatePolicy(ctx context.Context, user T) error {
	if g.policy == nil {
		return nil
	}
	return g.policy.ValidateUser(ctx, user)
}

func isNotFoundErr(err error) bool {
	if errors.Is(err, ErrUserNotFound) || errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if strings.HasSuffix(err.Error(), "no rows in result set") {
		return true
	}
	bizErr, ok := errs.As(err)
	return ok && bizErr.Code() == errs.CodeNotFound
}

// Manager 维护命名认证守卫（如 "client" 与 "admin"）的注册表。
type Manager struct {
	mu           sync.RWMutex
	defaultGuard string
	guards       map[string]StatefulGuard
}

// NewManager 创建多守卫管理器。
func NewManager() *Manager {
	return &Manager{
		guards: make(map[string]StatefulGuard),
	}
}

// Register 注册指定名称的守卫；首个注册的守卫自动成为默认守卫。
func (m *Manager) Register(name string, guard StatefulGuard) *Manager {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.guards[name] = guard
	if m.defaultGuard == "" {
		m.defaultGuard = name
	}
	return m
}

// SetDefault 设置默认守卫名称。
func (m *Manager) SetDefault(name string) *Manager {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultGuard = name
	return m
}

// Resolve 按名称解析守卫；未传名称时返回默认守卫。
func (m *Manager) Resolve(name ...string) (StatefulGuard, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	target := m.defaultGuard
	if len(name) > 0 && name[0] != "" {
		target = name[0]
	}
	if target == "" {
		return nil, fmt.Errorf("%w: no default guard configured", ErrGuardNotFound)
	}
	g, ok := m.guards[target]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrGuardNotFound, target)
	}
	return g, nil
}
