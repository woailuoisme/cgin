package authentication_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/internal/database/sqlc"
	"cgin/internal/enum"
	"cgin/internal/user"
	"cgin/pkg/authentication"
	"cgin/pkg/cache"
	"cgin/pkg/errs"
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
		Issuer:     "cgin-test",
		AccessTTL:  accessTTL,
		RefreshTTL: 24 * time.Hour,
	}, nil)
}

func newTestTokenIssuerWithBlacklist(accessTTL time.Duration, store authentication.BlacklistStore) *authentication.TokenIssuer {
	return authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     testSecret,
		Issuer:     "cgin-test",
		AccessTTL:  accessTTL,
		RefreshTTL: 24 * time.Hour,
	}, store)
}

type mockUserProvider struct {
	users         map[string]*sqlc.User
	loginHistory  []string
	validPass     bool
	equaliseCalls int
	lastEqualised string
}

func newMockUserProvider() *mockUserProvider {
	return &mockUserProvider{
		users:     make(map[string]*sqlc.User),
		validPass: true,
	}
}

func (p *mockUserProvider) FindByID(_ context.Context, id string) (*sqlc.User, error) {
	if u, ok := p.users[id]; ok {
		return u, nil
	}
	return nil, authentication.ErrUserNotFound
}

func (p *mockUserProvider) FindByCredentials(_ context.Context, creds authentication.Credentials) (*sqlc.User, error) {
	if u, ok := p.users[creds.Account]; ok {
		return u, nil
	}
	return nil, authentication.ErrUserNotFound
}

func (p *mockUserProvider) EqualiseTiming(_ context.Context, secret string) {
	p.equaliseCalls++
	p.lastEqualised = secret
}

func (p *mockUserProvider) ValidateCredentials(_ context.Context, _ *sqlc.User, _ authentication.Credentials) (bool, error) {
	return p.validPass, nil
}

func (p *mockUserProvider) RecordLogin(_ context.Context, id string, _ string) error {
	p.loginHistory = append(p.loginHistory, id)
	return nil
}

// TestGuardCheckAndID 测试 Check 状态判断及 ID 提取。
func TestGuardCheckAndID(t *testing.T) {
	guard := authentication.NewGuard(newTestTokenIssuer(15*time.Minute), nil, user.ClientPolicy{}, user.Identity)

	ctx := context.Background()
	assert.False(t, guard.Check(ctx))

	authCtx := authentication.With(ctx, authentication.Identity{
		UserID: "usr_123",
		Role:   "user",
	})
	assert.True(t, guard.Check(authCtx))

	userID, err := guard.ID(authCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_123", userID)

	_, err = guard.ID(ctx)
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthTokenInvalid, bizErr.Code())
}

// TestGuardUserAndUserOrFail 测试 User 与 UserOrFail 的空值与报错行为。
func TestGuardUserAndUserOrFail(t *testing.T) {
	provider := newMockUserProvider()
	provider.users["usr_db"] = &sqlc.User{
		ID:     "usr_db",
		Role:   enum.RoleUser,
		Status: enum.StatusActive,
		Name:   "DB User",
	}

	guard := authentication.NewGuard(newTestTokenIssuer(15*time.Minute), provider, user.ClientPolicy{}, user.Identity)
	ctx := context.Background()

	u, err := guard.User(ctx)
	require.NoError(t, err)
	assert.Nil(t, u)

	_, err = guard.UserOrFail(ctx)
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthTokenInvalid, bizErr.Code())

	mockUser := &sqlc.User{
		ID:     "usr_preset",
		Role:   enum.RoleUser,
		Status: enum.StatusActive,
		Name:   "Preset User",
	}
	presetCtx := authentication.WithUser(ctx, mockUser, user.Identity(mockUser))

	userGot, err := guard.User(presetCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_preset", userGot.ID)
	assert.Equal(t, "Preset User", userGot.Name)

	userOrFail, err := guard.UserOrFail(presetCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_preset", userOrFail.ID)

	idCtx := authentication.With(ctx, authentication.Identity{
		UserID: "usr_db",
		Role:   "user",
	})
	userFromDB, err := guard.User(idCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_db", userFromDB.ID)
	assert.Equal(t, "DB User", userFromDB.Name)
}

// TestGuardLoginAndClaims 测试 Login 签发与自定义声明提取。
func TestGuardLoginAndClaims(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	provider := newMockUserProvider()
	guard := authentication.NewGuard(issuer, provider, user.ClientPolicy{}, user.Identity)

	u := &sqlc.User{
		ID:     "usr_test_login",
		Role:   enum.RoleUser,
		Status: enum.StatusActive,
	}

	sess, err := guard.Login(context.Background(), u, authentication.WithClaims(map[string]any{
		"tenant_id": "tenant_abc",
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, sess.Token)
	assert.NotEmpty(t, sess.RefreshToken)
	assert.Positive(t, sess.ExpiresIn)

	claims, err := issuer.Parse(sess.Token, authentication.TypeAccess)
	require.NoError(t, err)
	assert.Equal(t, "usr_test_login", claims.Subject)

	val, ok := claims.GetClaim("tenant_id")
	require.True(t, ok)
	assert.Equal(t, "tenant_abc", val)

	payload := claims.Payload()
	assert.Equal(t, "usr_test_login", payload["sub"])
	assert.Equal(t, "tenant_abc", payload["tenant_id"])
}

// TestGuardLoginWithoutRefreshToken 校验 WithRefreshToken(false) 真正关闭刷新令牌签发。
func TestGuardLoginWithoutRefreshToken(t *testing.T) {
	guard := authentication.NewGuard(newTestTokenIssuer(15*time.Minute), newMockUserProvider(), user.ClientPolicy{}, user.Identity)
	u := &sqlc.User{
		ID:     "usr_no_refresh",
		Role:   enum.RoleUser,
		Status: enum.StatusActive,
	}

	sess, err := guard.Login(context.Background(), u, authentication.WithRefreshToken(false))
	require.NoError(t, err)
	assert.NotEmpty(t, sess.Token)
	assert.Empty(t, sess.RefreshToken, "关闭刷新令牌时不得返回 refresh token")
	assert.Positive(t, sess.ExpiresIn)

	_, _, err = guard.Refresh(context.Background(), "")
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthTokenInvalid, bizErr.Code())

	withRefresh, err := guard.Login(context.Background(), u)
	require.NoError(t, err)
	assert.NotEmpty(t, withRefresh.RefreshToken)
}

// TestGuardAttemptAndValidate 测试 Attempt 与 Validate 凭据认证契约。
func TestGuardAttemptAndValidate(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	provider := newMockUserProvider()
	provider.users["alex@example.com"] = &sqlc.User{
		ID:     "usr_alex",
		Email:  "alex@example.com",
		Role:   enum.RoleUser,
		Status: enum.StatusActive,
	}

	guard := authentication.NewGuard(issuer, provider, user.ClientPolicy{}, user.Identity)
	ctx := context.Background()

	loginUser, sess, err := guard.Attempt(ctx, authentication.Credentials{Account: "alex@example.com", Password: "correct"})
	require.NoError(t, err)
	assert.Equal(t, "usr_alex", loginUser.ID)
	assert.NotEmpty(t, sess.Token)
	assert.Contains(t, provider.loginHistory, "usr_alex")

	validUser, err := guard.Validate(ctx, authentication.Credentials{Account: "alex@example.com", Password: "correct"})
	require.NoError(t, err)
	assert.Equal(t, "usr_alex", validUser.ID)

	provider.validPass = false
	_, _, err = guard.Attempt(ctx, authentication.Credentials{Account: "alex@example.com", Password: "wrong"})
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthInvalidCredentials, bizErr.Code())

	_, err = guard.Validate(ctx, authentication.Credentials{Account: "alex@example.com", Password: "wrong"})
	require.Error(t, err)

	_, _, err = guard.Attempt(ctx, authentication.Credentials{Account: "notfound@example.com", Password: "any"})
	require.Error(t, err)
	bizErr, ok = errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthInvalidCredentials, bizErr.Code())
}

// TestGuardPolicyRejection 测试 AdminPolicy 对普通用户的拦截。
func TestGuardPolicyRejection(t *testing.T) {
	guard := authentication.NewGuard(newTestTokenIssuer(15*time.Minute), nil, user.AdminPolicy{}, user.Identity)

	normalUser := &sqlc.User{
		ID:     "usr_normal",
		Role:   enum.RoleUser,
		Status: enum.StatusActive,
	}

	_, err := guard.Login(context.Background(), normalUser)
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeForbidden, bizErr.Code())

	bannedAdmin := &sqlc.User{
		ID:     "usr_banned_admin",
		Role:   enum.RoleAdmin,
		Status: enum.StatusBanned,
	}
	_, err = guard.Login(context.Background(), bannedAdmin)
	require.Error(t, err)
	bizErr, ok = errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthAccountBanned, bizErr.Code())
}

// TestGuardRevocationEpochAndInvalidateAll 测试用户注销纪元与批量作废会话。
func TestGuardRevocationEpochAndInvalidateAll(t *testing.T) {
	store := newMockBlacklistStore()
	issuer := newTestTokenIssuerWithBlacklist(15*time.Minute, store)
	provider := newMockUserProvider()
	provider.users["usr_epoch"] = &sqlc.User{
		ID:     "usr_epoch",
		Role:   enum.RoleUser,
		Status: enum.StatusActive,
	}

	guard := authentication.NewGuard(issuer, provider, user.ClientPolicy{}, user.Identity)
	ctx := context.Background()

	sess, err := guard.Login(ctx, provider.users["usr_epoch"])
	require.NoError(t, err)

	authCtx, claims, err := guard.AuthenticateToken(ctx, sess.Token)
	require.NoError(t, err)
	assert.Equal(t, "usr_epoch", claims.Subject)
	assert.True(t, guard.Check(authCtx))

	err = guard.InvalidateAll(ctx, "usr_epoch")
	require.NoError(t, err)

	_, _, err = guard.AuthenticateToken(ctx, sess.Token)
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthTokenRevoked, bizErr.Code())
}

// TestGuardAuthenticateTokenPolicy 测试守卫在 AuthenticateToken 时执行策略校验及用户缓存。
func TestGuardAuthenticateTokenPolicy(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	provider := newMockUserProvider()
	provider.users["usr_admin"] = &sqlc.User{
		ID:     "usr_admin",
		Role:   enum.RoleAdmin,
		Status: enum.StatusActive,
		Name:   "Admin Guy",
	}
	provider.users["usr_normal"] = &sqlc.User{
		ID:     "usr_normal",
		Role:   enum.RoleUser,
		Status: enum.StatusActive,
		Name:   "Normal Guy",
	}
	provider.users["usr_banned"] = &sqlc.User{
		ID:     "usr_banned",
		Role:   enum.RoleAdmin,
		Status: enum.StatusBanned,
		Name:   "Banned Admin",
	}

	adminGuard := authentication.NewGuard(issuer, provider, user.AdminPolicy{}, user.Identity)
	ctx := context.Background()

	normalPair, err := issuer.Issue("usr_normal", "user")
	require.NoError(t, err)
	_, _, err = adminGuard.AuthenticateToken(ctx, normalPair.AccessToken)
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeForbidden, bizErr.Code())

	bannedPair, err := issuer.Issue("usr_banned", "admin")
	require.NoError(t, err)
	_, _, err = adminGuard.AuthenticateToken(ctx, bannedPair.AccessToken)
	require.Error(t, err)
	bizErr, ok = errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthAccountBanned, bizErr.Code())

	adminPair, err := issuer.Issue("usr_admin", "admin")
	require.NoError(t, err)
	authCtx, claims, err := adminGuard.AuthenticateToken(ctx, adminPair.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, "usr_admin", claims.Subject)

	u, err := adminGuard.User(authCtx)
	require.NoError(t, err)
	assert.Equal(t, "usr_admin", u.ID)
	assert.Equal(t, "Admin Guy", u.Name)
}

// TestAuthManagerResolve 测试多守卫注册与按名称解析。
func TestAuthManagerResolve(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	mgr := authentication.NewManager()

	_, err := mgr.Resolve()
	require.ErrorIs(t, err, authentication.ErrGuardNotFound)

	custom := authentication.NewGuard(issuer, nil, user.ClientPolicy{}, user.Identity)
	mgr.Register("custom", custom)

	sg, err := mgr.Resolve("custom")
	require.NoError(t, err)
	assert.Equal(t, custom, sg)

	_, err = mgr.Resolve("unknown")
	require.ErrorIs(t, err, authentication.ErrGuardNotFound)

	defSG, err := mgr.Resolve()
	require.NoError(t, err)
	assert.Equal(t, sg, defSG)
}

type testSubjectUser struct {
	id   string
	role string
	dept string
}

func (u *testSubjectUser) GetJWTCustomClaims() map[string]any {
	return map[string]any{"dept": u.dept}
}

func testSubjectIdentity(u *testSubjectUser) authentication.Identity {
	return authentication.Identity{
		UserID: u.id,
		Role:   u.role,
	}
}

// TestGuardSetUserAndUserFromContext 测试显式设置用户到上下文并按类型提取。
func TestGuardSetUserAndUserFromContext(t *testing.T) {
	guard := authentication.NewGuard(
		newTestTokenIssuer(15*time.Minute), newMockUserProvider(), user.ClientPolicy{}, user.Identity,
	)
	ctx := context.Background()

	assert.False(t, guard.Check(ctx))

	manualCtx := guard.SetUser(ctx, &sqlc.User{
		ID:     "usr_manual",
		Role:   "admin",
		Status: enum.StatusActive,
	})
	assert.True(t, guard.Check(manualCtx))

	uFromCtx, ok := authentication.UserFromContext[*sqlc.User](manualCtx)
	assert.True(t, ok)
	assert.Equal(t, "usr_manual", uFromCtx.ID)
}

// TestGuardJWTSubjectAutoClaims 测试实体实现 JWTSubject 契约时自动嵌入自定义声明。
func TestGuardJWTSubjectAutoClaims(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	guard := authentication.NewGuard(issuer, nil, nil, testSubjectIdentity)

	subUser := &testSubjectUser{
		id:   "usr_subject_1",
		role: "developer",
		dept: "engineering",
	}

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

// TestGuardValidateTimingAndAntiEnumeration 验证用户不存在时的时序平衡以及错误密码下不泄露封禁状态。
func TestGuardValidateTimingAndAntiEnumeration(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	provider := newMockUserProvider()
	provider.users["banned@example.com"] = &sqlc.User{
		ID:     "usr_banned_enum",
		Role:   enum.RoleUser,
		Status: enum.StatusBanned,
	}

	guard := authentication.NewGuard(issuer, provider, user.ClientPolicy{RequireEmailVerification: true}, user.Identity)
	ctx := t.Context()

	_, err := guard.Validate(ctx, authentication.Credentials{
		Account:  "ghost@example.com",
		Password: "GuessPass123!",
	})
	require.Error(t, err)
	assert.Equal(t, 1, provider.equaliseCalls)
	assert.Equal(t, "GuessPass123!", provider.lastEqualised)

	provider.validPass = false
	_, err = guard.Validate(ctx, authentication.Credentials{
		Account:  "banned@example.com",
		Password: "WrongPass!",
	})
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthInvalidCredentials, bizErr.Code())

	provider.validPass = true
	_, err = guard.Validate(ctx, authentication.Credentials{
		Account:  "banned@example.com",
		Password: "CorrectPass!",
	})
	require.Error(t, err)
	bizErr, ok = errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthAccountBanned, bizErr.Code())
}

// TestGuardResolveUser 验证从 Context 命中或按 ID 回源并执行策略校验。
func TestGuardResolveUser(t *testing.T) {
	issuer := newTestTokenIssuer(15 * time.Minute)
	provider := newMockUserProvider()
	provider.users["usr_resolve"] = &sqlc.User{
		ID:     "usr_resolve",
		Role:   enum.RoleAdmin,
		Status: enum.StatusActive,
	}

	guard := authentication.NewGuard(issuer, provider, user.AdminPolicy{}, user.Identity)
	ctx := t.Context()

	u, err := guard.ResolveUser(ctx, "usr_resolve")
	require.NoError(t, err)
	assert.Equal(t, "usr_resolve", u.ID)

	noProviderGuard := authentication.NewGuard(issuer, nil, user.AdminPolicy{}, user.Identity)
	cachedCtx := noProviderGuard.SetUser(ctx, u)
	uCached, err := noProviderGuard.ResolveUser(cachedCtx, "usr_resolve")
	require.NoError(t, err)
	assert.Equal(t, "usr_resolve", uCached.ID)
}
