package authentication

import (
	"context"
	"fmt"
	"maps"
	"time"
)

// Credentials 封装用户认证的凭证入参。
type Credentials struct {
	// Account 可为用户的电子邮箱地址、唯一登录用户名或手机号。
	Account string
	// Password 是未哈希的原始明文密码。
	Password string
	// Custom 承载扩展凭据字段（如图形验证码、MFA Token 等）。
	Custom map[string]any
}

// String 实现 fmt.Stringer，自动对密码字段进行安全脱敏。
func (c Credentials) String() string {
	return fmt.Sprintf("Credentials{Account:%q, Password:\"[REDACTED]\", Custom:%v}", c.Account, c.Custom)
}

// GoString 实现 fmt.GoStringer 保证调试与反射输出安全。
func (c Credentials) GoString() string {
	return c.String()
}

// UserProvider 抽象底层用户实体读取与凭据验证的契约，彻底解耦具体数据源与加密算法。
// T 不加约束：数据库行类型（如 sqlc 生成结构体）无需为平台实现任何方法。
type UserProvider[T any] interface {
	// FindByID 按主键查询主体；不存在时返回 pgx.ErrNoRows。
	FindByID(ctx context.Context, id string) (T, error)
	// FindByCredentials 根据凭据中的账号标识检索主体。
	FindByCredentials(ctx context.Context, creds Credentials) (T, error)
	// ValidateCredentials 校验指定主体是否与凭证匹配（如比对口令哈希）。
	ValidateCredentials(ctx context.Context, user T, creds Credentials) (bool, error)
}

// LoginRecorder 定义登录审计行为的可选扩展契约（遵循 ISP 接口隔离原则）。
// 当 UserProvider 实现此接口时，Guard.Attempt 登录成功后将自动触发审计记录。
type LoginRecorder interface {
	// RecordLogin 记录用户登录成功的时间与方式。
	RecordLogin(ctx context.Context, id string, method string) error
}

// Policy 定义守卫作用域的用户状态与权限校验契约。
type Policy[T any] interface {
	// ValidateUser 校验已检索出的主体是否满足守卫放行要求（如活跃状态、封禁限制、角色准入）。
	ValidateUser(ctx context.Context, user T) error
}

// PolicyFunc 允许以普通函数实现 Policy。
type PolicyFunc[T any] func(ctx context.Context, user T) error

// ValidateUser 执行函数式校验。
func (f PolicyFunc[T]) ValidateUser(ctx context.Context, user T) error {
	return f(ctx, user)
}

// AllowAllPolicy 默认放行所有实体的策略。
type AllowAllPolicy[T any] struct{}

// ValidateUser 默认无条件放行。
func (AllowAllPolicy[T]) ValidateUser(context.Context, T) error {
	return nil
}

// StatefulGuard 统一抽象传输层（HTTP 中间件、WebSocket 网关）与业务层通用的状态守卫契约。
type StatefulGuard interface {
	AuthenticateToken(ctx context.Context, rawToken string) (context.Context, *Claims, error)
	Check(ctx context.Context) bool
	InvalidateToken(ctx context.Context) error
}

// Gate 抽象统一的授权门禁契约 (Authorization Gate)。
// 贯穿上下文，实现业务层与中间件层统一的权限验证体验。
type Gate interface {
	// Can 判断当前请求主体是否对指定资源拥有对应动作的操作权限。
	Can(ctx context.Context, resource, action string) bool
	// Authorize 校验权限；若无权限则返回 403 Forbidden 错误。
	Authorize(ctx context.Context, resource, action string) error
	// Allows 是 Can 的语义别名。
	Allows(ctx context.Context, resource, action string) bool
	// Denies 判断当前请求主体是否被禁止该操作。
	Denies(ctx context.Context, resource, action string) bool
}

// Session 统一封装颁发的双令牌凭据与有效期元数据。
type Session struct {
	Token        string `json:"token" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`
	RefreshToken string `json:"refreshToken" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`
	ExpiresIn    int    `json:"expiresIn" example:"3600"`
}

// options 封装签发令牌时的自定义参数。
type options struct {
	ttl              time.Duration
	claims           map[string]any
	withRefreshToken bool
}

// defaultOptions 返回默认配置选项。
func defaultOptions() *options {
	return &options{
		withRefreshToken: true,
	}
}

// Option 定义 Guard 操作的函数式可选参数。
type Option func(*options)

// WithTTL 覆盖当前操作签发的访问令牌有效期。
func WithTTL(d time.Duration) Option {
	return func(o *options) {
		o.ttl = d
	}
}

// WithClaims 在当前操作签发的令牌中注入自定义声明键值对。
func WithClaims(claims map[string]any) Option {
	return func(o *options) {
		if o.claims == nil {
			o.claims = make(map[string]any, len(claims))
		}
		maps.Copy(o.claims, claims)
	}
}

// WithRefreshToken 控制是否生成刷新令牌。
func WithRefreshToken(enabled bool) Option {
	return func(o *options) {
		o.withRefreshToken = enabled
	}
}
