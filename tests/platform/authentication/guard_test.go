package authentication_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/database/models"
	"bgin/internal/platform/authentication"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/errs"
	"bgin/internal/shared/user"
)

const testSecret = "0123456789abcdef0123456789abcdef"

type mockBlacklistStore struct {
	mu   sync.RWMutex
	data map[string]string
}

func newMockBlacklistStore() *mockBlacklistStore {
	return &mockBlacklistStore{data: make(map[string]string)}
}

func (s *mockBlacklistStore) Get(_ context.Context, key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.data[key]
	if !ok {
		return "", cache.ErrNotFound
	}
	return val, nil
}

func (s *mockBlacklistStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *mockBlacklistStore) Exists(_ context.Context, key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[key]
	return ok, nil
}

func newTestTokenIssuer(accessTTL time.Duration) *authentication.TokenIssuer {
	return authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     testSecret,
		Issuer:     "bgin-test",
		AccessTTL:  accessTTL,
		RefreshTTL: 24 * time.Hour,
	}, nil)
}

func newTestTokenIssuerWithBlacklist(accessTTL time.Duration, store authentication.BlacklistStore) *authentication.TokenIssuer {
	return authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     testSecret,
		Issuer:     "bgin-test",
		AccessTTL:  accessTTL,
		RefreshTTL: 24 * time.Hour,
	}, store)
}

type mockUserProvider struct {
	users        map[string]*models.User
	loginHistory []string
	validPass    bool
}

func newMockUserProvider() *mockUserProvider {
	return &mockUserProvider{
		users:     make(map[string]*models.User),
		validPass: true,
	}
}

func (p *mockUserProvider) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := p.users[id]; ok {
		return u, nil
	}
	return nil, sql.ErrNoRows
}

func (p *mockUserProvider) FindByCredentials(_ context.Context, creds authentication.Credentials) (*models.User, error) {
	if u, ok := p.users[creds.Account]; ok {
		return u, nil
	}
	return nil, sql.ErrNoRows
}

func (p *mockUserProvider) ValidateCredentials(_ context.Context, _ *models.User, _ authentication.Credentials) (bool, error) {
	return p.validPass, nil
}

func (p *mockUserProvider) RecordLogin(_ context.Context, id string, _ string) error {
	p.loginHistory = append(p.loginHistory, id)
	return nil
}

// TestGuardCheckAndGuest 测试 Check 与 Guest 状态判断及 ID 提取。
func TestGuardCheckAndGuest(t *testing.T) {
	guard := authentication.NewGuard(newTestTokenIssuer(15*time.Minute), nil, user.ClientPolicy{})

	ctx := context.Background()
	assert.False(t, guard.Check(ctx))
	assert.True(t, guard.Guest(ctx))

	// 注入身份
	authCtx := authentication.With(ctx, authentication.Identity{
		UserID: "usr_123",
		Role:   "user",
	})
	assert.True(t, guard.Check(authCtx))
	assert.False(t, guard.Guest(authCtx))

	// 提取 ID
	userID, err := guard.ID(authCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_123", userID)

	// 未认证提取 ID 返回 401
	_, err = guard.ID(ctx)
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthTokenInvalid, bizErr.Code())
}

// TestGuardUserAndUserOrFail 测试 User 与 UserOrFail 的空值与报错行为。
func TestGuardUserAndUserOrFail(t *testing.T) {
	provider := newMockUserProvider()
	provider.users["usr_db"] = &models.User{
		ID:     "usr_db",
		Role:   models.RoleUser,
		Status: models.StatusActive,
		Name:   "DB User",
	}

	guard := authentication.NewGuard(newTestTokenIssuer(15*time.Minute), provider, user.ClientPolicy{})
	ctx := context.Background()

	// 访客调用 User 返回 (nil, nil)
	u, err := guard.User(ctx)
	require.NoError(t, err)
	assert.Nil(t, u)

	// 访客调用 UserOrFail 返回 401
	_, err = guard.UserOrFail(ctx)
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthTokenInvalid, bizErr.Code())

	// 预置 WithUser
	mockUser := &models.User{
		ID:     "usr_preset",
		Role:   models.RoleUser,
		Status: models.StatusActive,
		Name:   "Preset User",
	}
	presetCtx := authentication.WithUser(ctx, mockUser)

	userGot, err := guard.User(presetCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_preset", userGot.ID)
	assert.Equal(t, "Preset User", userGot.Name)

	userOrFail, err := guard.UserOrFail(presetCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_preset", userOrFail.ID)

	// 仅含 Identity 时，通过 Provider 按需回源加载
	idCtx := authentication.With(ctx, authentication.Identity{
		UserID: "usr_db",
		Role:   "user",
	})
	userFromDB, err := guard.User(idCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_db", userFromDB.ID)
	assert.Equal(t, "DB User", userFromDB.Name)
}

// TestGuardLoginAndClaims 测试 Login 签发与 Claims / Payload 提取。
func TestGuardLoginAndClaims(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	provider := newMockUserProvider()
	guard := authentication.NewGuard(issuer, provider, user.ClientPolicy{})

	u := &models.User{
		ID:     "usr_test_login",
		Role:   models.RoleUser,
		Status: models.StatusActive,
	}

	sess, err := guard.Login(context.Background(), u, authentication.WithClaims(map[string]any{
		"tenant_id": "tenant_abc",
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, sess.Token)
	assert.NotEmpty(t, sess.RefreshToken)
	assert.Positive(t, sess.ExpiresIn)

	// 解析令牌并放入上下文
	claims, err := issuer.Parse(sess.Token, authentication.TypeAccess)
	require.NoError(t, err)

	ctx := authentication.With(context.Background(), authentication.Identity{
		UserID: claims.Subject,
		Role:   claims.Role,
		Claims: claims,
	})

	// 提取 Claims
	cGot, err := guard.Claims(ctx)
	require.NoError(t, err)
	assert.Equal(t, "usr_test_login", cGot.Subject)

	// 提取自定义 Claim
	val, err := guard.GetClaim(ctx, "tenant_id")
	require.NoError(t, err)
	assert.Equal(t, "tenant_abc", val)

	// Payload 转换
	payload, err := guard.Payload(ctx)
	require.NoError(t, err)
	assert.Equal(t, "usr_test_login", payload["sub"])
	assert.Equal(t, "tenant_abc", payload["tenant_id"])
}

// TestGuardAttemptAndValidate 测试 Attempt 与 Validate 凭据认证契约。
func TestGuardAttemptAndValidate(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	provider := newMockUserProvider()
	provider.users["alex@example.com"] = &models.User{
		ID:     "usr_alex",
		Email:  "alex@example.com",
		Role:   models.RoleUser,
		Status: models.StatusActive,
	}

	guard := authentication.NewGuard(issuer, provider, user.ClientPolicy{})
	ctx := context.Background()

	// 凭据有效时的 Attempt
	loginUser, sess, err := guard.Attempt(ctx, authentication.Credentials{Account: "alex@example.com", Password: "correct"})
	require.NoError(t, err)
	assert.Equal(t, "usr_alex", loginUser.ID)
	assert.NotEmpty(t, sess.Token)
	assert.Contains(t, provider.loginHistory, "usr_alex")

	// Validate 仅校验凭据，不签发令牌
	validUser, err := guard.Validate(ctx, authentication.Credentials{Account: "alex@example.com", Password: "correct"})
	require.NoError(t, err)
	assert.Equal(t, "usr_alex", validUser.ID)

	// 口令错误时的 Attempt
	provider.validPass = false
	_, _, err = guard.Attempt(ctx, authentication.Credentials{Account: "alex@example.com", Password: "wrong"})
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthInvalidCredentials, bizErr.Code())

	_, err = guard.Validate(ctx, authentication.Credentials{Account: "alex@example.com", Password: "wrong"})
	require.Error(t, err)

	// 用户不存在时的 Attempt
	_, _, err = guard.Attempt(ctx, authentication.Credentials{Account: "notfound@example.com", Password: "any"})
	require.Error(t, err)
	bizErr, ok = errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthInvalidCredentials, bizErr.Code())
}

// TestGuardLoginUsingIDAndTokenByID 测试按 ID 登录与原始令牌生成。
func TestGuardLoginUsingIDAndTokenByID(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	provider := newMockUserProvider()
	provider.users["usr_target"] = &models.User{
		ID:     "usr_target",
		Role:   models.RoleUser,
		Status: models.StatusActive,
	}

	guard := authentication.NewGuard(issuer, provider, user.ClientPolicy{})
	ctx := context.Background()

	u, sess, err := guard.LoginUsingID(ctx, "usr_target")
	require.NoError(t, err)
	assert.Equal(t, "usr_target", u.ID)
	assert.NotEmpty(t, sess.Token)

	rawToken, err := guard.TokenByID(ctx, "usr_target")
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken)

	// 用户不存在返回 404
	_, _, err = guard.LoginUsingID(ctx, "usr_unknown")
	require.Error(t, err)
}

// TestGuardPolicyRejection 测试 AdminPolicy 对普通用户的拦截。
func TestGuardPolicyRejection(t *testing.T) {
	guard := authentication.NewGuard(newTestTokenIssuer(15*time.Minute), nil, user.AdminPolicy{})

	normalUser := &models.User{
		ID:     "usr_normal",
		Role:   models.RoleUser, // 非 admin 角色
		Status: models.StatusActive,
	}

	// 尝试以普通用户身份登录 AdminGuard 应被策略拒绝
	_, err := guard.Login(context.Background(), normalUser)
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeForbidden, bizErr.Code())

	// 封禁账号应被拒绝
	bannedAdmin := &models.User{
		ID:     "usr_banned_admin",
		Role:   models.RoleAdmin,
		Status: models.StatusBanned,
	}
	_, err = guard.Login(context.Background(), bannedAdmin)
	require.Error(t, err)
	bizErr, ok = errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthAccountBanned, bizErr.Code())
}

// TestGuardSetTTLAndSetClaims 测试 Guard 的链式克隆与 SetToken。
func TestGuardSetTTLAndSetClaims(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	guard := authentication.NewGuard(issuer, nil, user.ClientPolicy{})

	customGuard := guard.SetTTL(2 * time.Hour).SetClaims(map[string]any{
		"scope": "read:all",
	})

	u := &models.User{
		ID:     "usr_clone",
		Role:   models.RoleUser,
		Status: models.StatusActive,
	}

	sess, err := customGuard.Login(context.Background(), u)
	require.NoError(t, err)

	// 解析验证有效期接近 2 小时 (7200 秒)
	claims, err := issuer.Parse(sess.Token, authentication.TypeAccess)
	require.NoError(t, err)
	assert.Equal(t, "read:all", claims.Custom["scope"])
	assert.InDelta(t, 7200, sess.ExpiresIn, 5)

	// 测试 SetToken 显式绑定
	boundCtx, err := guard.SetToken(context.Background(), sess.Token)
	require.NoError(t, err)
	assert.True(t, guard.Check(boundCtx))
	id, err := guard.ID(boundCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_clone", id)

	claims, err = guard.Claims(boundCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_clone", claims.Subject)
}

// TestAuthManager 测试多守卫注册与解析。
func TestAuthManager(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	clientGuard := authentication.NewGuard(issuer, nil, user.ClientPolicy{})
	adminGuard := authentication.NewGuard(issuer, nil, user.AdminPolicy{})

	mgr := authentication.NewManager()
	mgr.Register("client", clientGuard)
	mgr.Register("admin", adminGuard)
	mgr.SetDefault("client")

	// 默认守卫解析
	defGuard, err := mgr.Default()
	require.NoError(t, err)
	assert.Equal(t, clientGuard, defGuard)

	// 指定守卫解析
	adGuard, err := mgr.Guard("admin")
	require.NoError(t, err)
	assert.Equal(t, adminGuard, adGuard)

	// 类型安全辅助函数
	typedGuard, ok := authentication.GetGuard[*models.User](mgr, "client")
	assert.True(t, ok)
	assert.Equal(t, clientGuard, typedGuard)

	// MustGuard
	assert.Equal(t, adminGuard, mgr.MustGuard("admin"))

	// 未知守卫
	_, err = mgr.Guard("unknown")
	assert.Error(t, err)
}

// TestGuardRevocationEpochAndInvalidateAll 测试用户注销纪元与批量作废会话。
func TestGuardRevocationEpochAndInvalidateAll(t *testing.T) {
	store := newMockBlacklistStore()
	issuer := newTestTokenIssuerWithBlacklist(15*time.Minute, store)
	provider := newMockUserProvider()
	provider.users["usr_epoch"] = &models.User{
		ID:     "usr_epoch",
		Role:   models.RoleUser,
		Status: models.StatusActive,
	}

	guard := authentication.NewGuard(issuer, provider, user.ClientPolicy{})
	ctx := context.Background()

	sess, err := guard.Login(ctx, provider.users["usr_epoch"])
	require.NoError(t, err)

	// 初始状态下令牌有效
	authCtx, claims, err := guard.AuthenticateToken(ctx, sess.Token)
	require.NoError(t, err)
	assert.Equal(t, "usr_epoch", claims.Subject)
	assert.True(t, guard.Check(authCtx))

	// 触发用户级注销纪元（InvalidateAll）
	err = guard.InvalidateAll(ctx, "usr_epoch")
	require.NoError(t, err)

	// 原令牌认证被拒，返回 401 撤销错误
	_, _, err = guard.AuthenticateToken(ctx, sess.Token)
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthTokenRevoked, bizErr.Code())

	// 测试 LogoutAll 在已认证上下文中的调用
	user2 := &models.User{
		ID:     "usr_epoch2",
		Role:   models.RoleUser,
		Status: models.StatusActive,
	}
	provider.users["usr_epoch2"] = user2
	sess2, err := guard.Login(ctx, user2)
	require.NoError(t, err)

	authCtx2, _, err := guard.AuthenticateToken(ctx, sess2.Token)
	require.NoError(t, err)

	err = guard.LogoutAll(authCtx2)
	require.NoError(t, err)

	_, _, err = guard.AuthenticateToken(ctx, sess2.Token)
	require.Error(t, err)
	bizErr, ok = errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthTokenRevoked, bizErr.Code())
}

// TestGuardAuthenticateTokenPolicy 测试守卫在 AuthenticateToken 时执行策略校验及用户缓存。
func TestGuardAuthenticateTokenPolicy(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	provider := newMockUserProvider()
	provider.users["usr_admin"] = &models.User{
		ID:     "usr_admin",
		Role:   models.RoleAdmin,
		Status: models.StatusActive,
		Name:   "Admin Guy",
	}
	provider.users["usr_normal"] = &models.User{
		ID:     "usr_normal",
		Role:   models.RoleUser,
		Status: models.StatusActive,
		Name:   "Normal Guy",
	}
	provider.users["usr_banned"] = &models.User{
		ID:     "usr_banned",
		Role:   models.RoleAdmin,
		Status: models.StatusBanned,
		Name:   "Banned Admin",
	}

	adminGuard := authentication.NewGuard(issuer, provider, user.AdminPolicy{})
	ctx := context.Background()

	// 1. 普通用户访问 AdminGuard 应被策略拒绝（返回 CodeForbidden）
	normalPair, err := issuer.Issue("usr_normal", "user")
	require.NoError(t, err)
	_, _, err = adminGuard.AuthenticateToken(ctx, normalPair.AccessToken)
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeForbidden, bizErr.Code())

	// 2. 封禁管理员访问 AdminGuard 应被策略拒绝（返回 CodeAuthAccountBanned）
	bannedPair, err := issuer.Issue("usr_banned", "admin")
	require.NoError(t, err)
	_, _, err = adminGuard.AuthenticateToken(ctx, bannedPair.AccessToken)
	require.Error(t, err)
	bizErr, ok = errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthAccountBanned, bizErr.Code())

	// 3. 正常管理员访问 AdminGuard 认证通过，且上下文缓存被填充（后续 User 0 次 DB 查询）
	adminPair, err := issuer.Issue("usr_admin", "admin")
	require.NoError(t, err)
	authCtx, claims, err := adminGuard.AuthenticateToken(ctx, adminPair.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, "usr_admin", claims.Subject)

	// 从 authCtx 提取用户实体，验证为 Admin Guy 且无需再次查库
	u, err := adminGuard.User(authCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_admin", u.ID)
	assert.Equal(t, "Admin Guy", u.Name)
}

// TestAuthManagerExtendAndResolve 测试 AuthManager 驱动扩展与动态解析。
func TestAuthManagerExtendAndResolve(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	mgr := authentication.NewManager()

	instantiated := false
	mgr.Extend("custom", func() (authentication.StatefulGuard, error) {
		instantiated = true
		return authentication.NewGuard(issuer, nil, user.ClientPolicy{}), nil
	})

	assert.False(t, instantiated, "未调用前不应提前实例化")

	// Resolve("custom") 触发延迟创建
	sg, err := mgr.Resolve("custom")
	require.NoError(t, err)
	assert.True(t, instantiated)
	assert.NotNil(t, sg)

	// Resolve 未知 Guard 返回 ErrGuardNotFound
	_, err = mgr.Resolve("unknown")
	require.ErrorIs(t, err, authentication.ErrGuardNotFound)

	// Resolve 默认 Guard，未配置默认守卫时返回错误
	_, err = mgr.Resolve()
	require.ErrorIs(t, err, authentication.ErrGuardNotFound)

	// 设置默认守卫后成功解析
	mgr.SetDefault("custom")
	defSG, err := mgr.Resolve()
	require.NoError(t, err)
	assert.Equal(t, sg, defSG)
}

type testSubjectUser struct {
	id   string
	role string
	dept string
}

func (u *testSubjectUser) GetAuthIdentifier() string {
	return u.id
}

func (u *testSubjectUser) GetAuthRole() string {
	return u.role
}

func (u *testSubjectUser) GetJWTCustomClaims() map[string]any {
	return map[string]any{
		"dept": u.dept,
	}
}

// TestGuardOnceAndOnceUsingID 测试类似 Laravel 的单请求认证方法（不签发令牌，直接置入上下文）。
func TestGuardOnceAndOnceUsingID(t *testing.T) {
	provider := newMockUserProvider()
	provider.users["usr_alex"] = &models.User{
		ID:     "usr_alex",
		Email:  "alex@example.com",
		Role:   "user",
		Status: models.StatusActive,
	}

	guard := authentication.NewGuard(newTestTokenIssuer(15*time.Minute), provider, user.ClientPolicy{})
	ctx := context.Background()

	// 初始状态：未认证且无实体缓存
	assert.False(t, guard.Check(ctx))
	assert.False(t, guard.HasUser(ctx))

	// 1. Once: 密码匹配，单次认证成功，返回带有用户的 newCtx
	newCtx, u, err := guard.Once(ctx, authentication.Credentials{
		Account:  "usr_alex",
		Password: "password123",
	})
	require.NoError(t, err)
	assert.Equal(t, "usr_alex", u.ID)

	// 验证 newCtx 处于已认证状态，且 HasUser 为 true
	assert.True(t, guard.Check(newCtx))
	assert.True(t, guard.HasUser(newCtx))

	// 验证 User 从缓存提取（0 次额外 DB 查询）
	cachedUser, err := guard.User(newCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_alex", cachedUser.ID)

	// 验证泛型辅助函数 UserFromContext
	typedUser, ok := authentication.UserFromContext[*models.User](newCtx)
	assert.True(t, ok)
	assert.Equal(t, "usr_alex", typedUser.ID)

	// 2. Once: 凭证错误，认证失败，不污染上下文
	provider.validPass = false
	failCtx, _, err := guard.Once(ctx, authentication.Credentials{
		Account:  "usr_alex",
		Password: "wrong",
	})
	require.Error(t, err)
	assert.False(t, guard.Check(failCtx))
	assert.False(t, guard.HasUser(failCtx))
	provider.validPass = true

	// 3. OnceUsingID: 按用户 ID 单次认证成功
	idCtx, idUser, err := guard.OnceUsingID(ctx, "usr_alex")
	require.NoError(t, err)
	assert.Equal(t, "usr_alex", idUser.ID)
	assert.True(t, guard.Check(idCtx))
	assert.True(t, guard.HasUser(idCtx))

	// 4. SetUser: 显式设置用户到上下文
	manualUser := &models.User{
		ID:     "usr_manual",
		Role:   "admin",
		Status: models.StatusActive,
	}
	manualCtx := guard.SetUser(ctx, manualUser)
	assert.True(t, guard.Check(manualCtx))
	assert.True(t, guard.HasUser(manualCtx))
	uFromCtx, ok := authentication.UserFromContext[*models.User](manualCtx)
	assert.True(t, ok)
	assert.Equal(t, "usr_manual", uFromCtx.ID)
}

// TestGuardJWTSubjectAutoClaims 测试实体实现 JWTSubject 契约时自动嵌入自定义声明（对应 jwt-auth）。
func TestGuardJWTSubjectAutoClaims(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	guard := authentication.NewGuard(issuer, nil, authentication.AllowAllPolicy[*testSubjectUser]{})

	subUser := &testSubjectUser{
		id:   "usr_subject_1",
		role: "developer",
		dept: "engineering",
	}

	// 1. Login 自动提取 JWTSubject 声明
	sess, err := guard.Login(context.Background(), subUser)
	require.NoError(t, err)
	assert.NotEmpty(t, sess.Token)

	claims, err := issuer.Parse(sess.Token, authentication.TypeAccess)
	require.NoError(t, err)
	assert.Equal(t, "usr_subject_1", claims.Subject)
	assert.Equal(t, "developer", claims.Role)
	val, ok := claims.GetClaim("dept")
	assert.True(t, ok)
	assert.Equal(t, "engineering", val)

	// 2. 与 WithClaims 叠加时两者合并
	sessWithMore, err := guard.Login(context.Background(), subUser, authentication.WithClaims(map[string]any{
		"team": "core-infra",
	}))
	require.NoError(t, err)
	claims2, err := issuer.Parse(sessWithMore.Token, authentication.TypeAccess)
	require.NoError(t, err)
	valDept, _ := claims2.GetClaim("dept")
	valTeam, _ := claims2.GetClaim("team")
	assert.Equal(t, "engineering", valDept)
	assert.Equal(t, "core-infra", valTeam)
}
