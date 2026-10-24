// Package auth_test 验证后台管理员认证域的核心业务逻辑与契约。
package auth_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/database/models"
	adminauth "bgin/internal/features/admin/auth"
	"bgin/internal/platform/authentication"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/hasher"
	"bgin/internal/platform/validator"
	"bgin/internal/shared/enums"
	shareduser "bgin/internal/shared/user"
)

func init() {
	_ = validator.InitEngine()
}

const testJWTSecret = "0123456789abcdef0123456789abcdef"

// mockRepo 模拟管理员认证数据仓储。
type mockRepo struct {
	users        map[string]*models.User
	accounts     map[string]*models.Account
	loginRecords []string
}

func newMockRepo() *mockRepo {
	return &mockRepo{
		users:    make(map[string]*models.User),
		accounts: make(map[string]*models.Account),
	}
}

func (m *mockRepo) FindByCredentials(ctx context.Context, creds authentication.Credentials) (*models.User, error) {
	return m.FindByEmail(ctx, creds.Account)
}

func (m *mockRepo) FindCredentialPassword(_ context.Context, userID string) (*string, error) {
	if a, ok := m.accounts[userID]; ok {
		return a.Password, nil
	}
	return nil, sql.ErrNoRows
}

func (m *mockRepo) ValidateCredentials(_ context.Context, u *models.User, creds authentication.Credentials) (bool, error) {
	if a, ok := m.accounts[u.ID]; ok && a.Password != nil {
		h := hasher.New()
		return h.Check(*a.Password, creds.Password)
	}
	return false, nil
}

func (m *mockRepo) FindByEmail(_ context.Context, email string) (*models.User, error) {
	for _, u := range m.users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (m *mockRepo) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := m.users[id]; ok {
		return u, nil
	}
	return nil, sql.ErrNoRows
}

func (m *mockRepo) FindCredentialAccount(_ context.Context, userID string) (*models.Account, error) {
	if a, ok := m.accounts[userID]; ok {
		return a, nil
	}
	return nil, sql.ErrNoRows
}

func (m *mockRepo) UpdatePassword(_ context.Context, userID, passwordHash string) error {
	if a, ok := m.accounts[userID]; ok {
		a.Password = &passwordHash
		return nil
	}
	return sql.ErrNoRows
}

func (m *mockRepo) RecordLogin(_ context.Context, userID, _ string) error {
	m.loginRecords = append(m.loginRecords, userID)
	return nil
}

func setupService(repo *mockRepo) (*adminauth.Service, *authentication.TokenIssuer) {
	h := hasher.New()
	tokens := authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     testJWTSecret,
		Issuer:     "bgin-test",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	}, nil)

	guard := authentication.NewGuard(tokens, repo, shareduser.AdminPolicy{})
	service := adminauth.NewService(adminauth.Deps{
		Guard:  guard,
		Tokens: tokens,
		Hasher: h,
	})
	return service, tokens
}

// TestAdminSignInEmailAntiEnumeration 校验登录防枚举策略：
// 邮箱不存在、密码错误、普通用户角色均统一返回凭据错误。
func TestAdminSignInEmailAntiEnumeration(t *testing.T) {
	repo := newMockRepo()
	h := hasher.New()
	adminHash, err := h.Make("Admin1234!")
	require.NoError(t, err)

	userHash, err := h.Make("User1234!")
	require.NoError(t, err)

	// 管理员账号
	repo.users["admin_1"] = &models.User{
		ID:     "admin_1",
		Email:  "admin@example.com",
		Role:   models.RoleAdmin,
		Status: models.StatusActive,
	}
	repo.accounts["admin_1"] = &models.Account{
		UserID:     "admin_1",
		ProviderID: models.ProviderCredential,
		Password:   &adminHash,
	}

	// 普通用户账号
	repo.users["user_1"] = &models.User{
		ID:     "user_1",
		Email:  "user@example.com",
		Role:   models.RoleUser,
		Status: models.StatusActive,
	}
	repo.accounts["user_1"] = &models.Account{
		UserID:     "user_1",
		ProviderID: models.ProviderCredential,
		Password:   &userHash,
	}

	service, _ := setupService(repo)
	ctx := t.Context()

	// 1. 邮箱不存在
	_, err = service.SignInEmail(ctx, adminauth.SignInEmailRequest{
		Email:    "nonexistent@example.com",
		Password: "Password123!",
	})
	require.Error(t, err)
	_, resp := errs.Map(err)
	assert.Equal(t, string(errs.CodeAuthInvalidCredentials), resp.Code)

	// 2. 普通用户尝试从管理端登录（必须伪装为凭据错误，防止角色与账号枚举）
	_, err = service.SignInEmail(ctx, adminauth.SignInEmailRequest{
		Email:    "user@example.com",
		Password: "User1234!",
	})
	require.Error(t, err)
	_, resp = errs.Map(err)
	assert.Equal(t, string(errs.CodeAuthInvalidCredentials), resp.Code)

	// 3. 管理员密码错误
	_, err = service.SignInEmail(ctx, adminauth.SignInEmailRequest{
		Email:    "admin@example.com",
		Password: "WrongPassword!",
	})
	require.Error(t, err)
	_, resp = errs.Map(err)
	assert.Equal(t, string(errs.CodeAuthInvalidCredentials), resp.Code)

	// 4. 管理员凭据正确登录成功
	session, err := service.SignInEmail(ctx, adminauth.SignInEmailRequest{
		Email:    "admin@example.com",
		Password: "Admin1234!",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, session.Token)
	assert.NotEmpty(t, session.RefreshToken)
	assert.Equal(t, "admin_1", session.User.ID)
	assert.Equal(t, enums.RoleAdmin, session.User.Role)
	assert.Contains(t, repo.loginRecords, "admin_1")
}

// TestAdminSignInBannedAndInactive 校验封禁与停用状态拦截。
func TestAdminSignInBannedAndInactive(t *testing.T) {
	repo := newMockRepo()
	h := hasher.New()
	hash, err := h.Make("Admin1234!")
	require.NoError(t, err)

	repo.users["banned_admin"] = &models.User{
		ID:     "banned_admin",
		Email:  "banned@example.com",
		Role:   models.RoleAdmin,
		Status: models.StatusBanned,
	}
	repo.accounts["banned_admin"] = &models.Account{
		UserID:     "banned_admin",
		ProviderID: models.ProviderCredential,
		Password:   &hash,
	}

	repo.users["inactive_admin"] = &models.User{
		ID:     "inactive_admin",
		Email:  "inactive@example.com",
		Role:   models.RoleAdmin,
		Status: models.StatusInactive,
	}
	repo.accounts["inactive_admin"] = &models.Account{
		UserID:     "inactive_admin",
		ProviderID: models.ProviderCredential,
		Password:   &hash,
	}

	service, _ := setupService(repo)
	ctx := t.Context()

	// 封禁账号拦截
	_, err = service.SignInEmail(ctx, adminauth.SignInEmailRequest{
		Email:    "banned@example.com",
		Password: "Admin1234!",
	})
	require.Error(t, err)
	_, resp := errs.Map(err)
	assert.Equal(t, string(errs.CodeAuthAccountBanned), resp.Code)

	// 未激活账号拦截
	_, err = service.SignInEmail(ctx, adminauth.SignInEmailRequest{
		Email:    "inactive@example.com",
		Password: "Admin1234!",
	})
	require.Error(t, err)
	status, _ := errs.Map(err)
	assert.Equal(t, 403, status)
}

// TestAdminGetSessionAndRoleCheck 校验会话获取与管理员角色约束。
func TestAdminGetSessionAndRoleCheck(t *testing.T) {
	repo := newMockRepo()
	repo.users["admin_1"] = &models.User{
		ID:     "admin_1",
		Email:  "admin@example.com",
		Role:   models.RoleAdmin,
		Status: models.StatusActive,
	}
	repo.users["user_1"] = &models.User{
		ID:     "user_1",
		Email:  "user@example.com",
		Role:   models.RoleUser,
		Status: models.StatusActive,
	}

	service, _ := setupService(repo)
	ctx := t.Context()

	// 正常管理员会话
	res, err := service.GetSession(ctx, "admin_1")
	require.NoError(t, err)
	assert.Equal(t, "admin_1", res.User.ID)
	assert.Equal(t, enums.RoleAdmin, res.User.Role)

	// 非管理员请求管理端会话，拦截 403
	_, err = service.GetSession(ctx, "user_1")
	require.Error(t, err)
	status, _ := errs.Map(err)
	assert.Equal(t, 403, status)

	// 不存在的用户，返回 401
	_, err = service.GetSession(ctx, "unknown")
	require.Error(t, err)
	status, _ = errs.Map(err)
	assert.Equal(t, 401, status)
}

// TestAdminRefreshDemotedUserDenied 校验令牌刷新时若被降权为普通用户则拒绝续签。
func TestAdminRefreshDemotedUserDenied(t *testing.T) {
	repo := newMockRepo()
	repo.users["demoted"] = &models.User{
		ID:     "demoted",
		Email:  "demoted@example.com",
		Role:   models.RoleUser, // 降权为普通用户
		Status: models.StatusActive,
	}

	service, tokens := setupService(repo)
	ctx := t.Context()

	pair, err := tokens.Issue("demoted", string(models.RoleAdmin))
	require.NoError(t, err)

	_, err = service.Refresh(ctx, adminauth.RefreshTokenRequest{
		RefreshToken: pair.RefreshToken,
	})
	require.Error(t, err)
	status, _ := errs.Map(err)
	assert.Equal(t, 403, status)
}

// TestAdminChangePasswordFlow 校验管理员修改密码。
func TestAdminChangePasswordFlow(t *testing.T) {
	repo := newMockRepo()
	h := hasher.New()
	hash, err := h.Make("OldAdminPass123!")
	require.NoError(t, err)

	repo.users["admin_1"] = &models.User{
		ID:     "admin_1",
		Email:  "admin@example.com",
		Role:   models.RoleAdmin,
		Status: models.StatusActive,
	}
	repo.accounts["admin_1"] = &models.Account{
		UserID:     "admin_1",
		ProviderID: models.ProviderCredential,
		Password:   &hash,
	}

	service, _ := setupService(repo)
	ctx := t.Context()

	// 1. 新密码强度不足
	err = service.ChangePassword(ctx, "admin_1", adminauth.ChangePasswordRequest{
		CurrentPassword: "OldAdminPass123!",
		NewPassword:     "short",
	})
	require.Error(t, err)
	_, resp := errs.Map(err)
	assert.Equal(t, string(errs.CodeAuthPasswordTooWeak), resp.Code)

	// 2. 当前旧密码不匹配
	err = service.ChangePassword(ctx, "admin_1", adminauth.ChangePasswordRequest{
		CurrentPassword: "WrongOldPass123!",
		NewPassword:     "NewAdminPass456!",
	})
	require.Error(t, err)
	_, resp = errs.Map(err)
	assert.Equal(t, string(errs.CodeAuthPasswordMismatch), resp.Code)

	// 3. 修改成功
	err = service.ChangePassword(ctx, "admin_1", adminauth.ChangePasswordRequest{
		CurrentPassword: "OldAdminPass123!",
		NewPassword:     "NewAdminPass456!",
	})
	require.NoError(t, err)

	// 验证库中密码哈希已更新
	acc, err := repo.FindCredentialAccount(ctx, "admin_1")
	require.NoError(t, err)
	matched, err := h.Check(*acc.Password, "NewAdminPass456!")
	require.NoError(t, err)
	assert.True(t, matched)

	// 4. 普通用户越权请求管理端修改口令，拦截 403
	repo.users["user_1"] = &models.User{
		ID:     "user_1",
		Email:  "user@example.com",
		Role:   models.RoleUser,
		Status: models.StatusActive,
	}
	repo.accounts["user_1"] = &models.Account{
		UserID:     "user_1",
		ProviderID: models.ProviderCredential,
		Password:   &hash,
	}
	err = service.ChangePassword(ctx, "user_1", adminauth.ChangePasswordRequest{
		CurrentPassword: "OldAdminPass123!",
		NewPassword:     "NewAdminPass456!",
	})
	require.Error(t, err)
	status, _ := errs.Map(err)
	assert.Equal(t, 403, status)

	// 5. 封禁管理员请求修改口令，拦截 403 / AccountBanned
	repo.users["banned_admin"] = &models.User{
		ID:     "banned_admin",
		Email:  "banned@example.com",
		Role:   models.RoleAdmin,
		Status: models.StatusBanned,
	}
	repo.accounts["banned_admin"] = &models.Account{
		UserID:     "banned_admin",
		ProviderID: models.ProviderCredential,
		Password:   &hash,
	}
	err = service.ChangePassword(ctx, "banned_admin", adminauth.ChangePasswordRequest{
		CurrentPassword: "OldAdminPass123!",
		NewPassword:     "NewAdminPass456!",
	})
	require.Error(t, err)
	_, resp = errs.Map(err)
	assert.Equal(t, string(errs.CodeAuthAccountBanned), resp.Code)

	// 6. 不存在用户拦截 401
	err = service.ChangePassword(ctx, "nonexistent", adminauth.ChangePasswordRequest{
		CurrentPassword: "OldAdminPass123!",
		NewPassword:     "NewAdminPass456!",
	})
	require.Error(t, err)
	status, _ = errs.Map(err)
	assert.Equal(t, 401, status)
}

// TestAdminAuthDTOSerialization 校验 DTO 的 JSON 序列化与反序列化字段契约。
func TestAdminAuthDTOSerialization(t *testing.T) {
	raw := []byte(`{"email":"admin@bgin.local","password":"secret"}`)
	var req adminauth.SignInEmailRequest
	require.NoError(t, json.Unmarshal(raw, &req))
	assert.Equal(t, "admin@bgin.local", req.Email)
	assert.Equal(t, "secret", req.Password)

	signOutRaw := []byte(`{"refreshToken":"rt_test123"}`)
	var signOutReq adminauth.SignOutRequest
	require.NoError(t, json.Unmarshal(signOutRaw, &signOutReq))
	require.NotNil(t, signOutReq.RefreshToken)
	assert.Equal(t, "rt_test123", *signOutReq.RefreshToken)
}
