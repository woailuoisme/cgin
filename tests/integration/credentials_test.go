//go:build integration

package integration_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/database/sqlc"
	"cgin/pkg/errs"
	"cgin/pkg/hasher"
	"cgin/shared/enums"
	shareduser "cgin/shared/user"
)

// TestRegisterCreatesUserAndCredential 覆盖注册深模块的事务写入、归一化与默认值。
func TestRegisterCreatesUserAndCredential(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	h := hasher.New()
	service := shareduser.MustNewService(db, h)
	provider := shareduser.MustNewProvider(db, h)

	email := strings.ToUpper(uniqueEmail())
	username := strings.ToUpper(uniqueUsername())

	created, err := service.Register(ctx, shareduser.RegisterParams{
		Name:     "  Integration Tester  ",
		Email:    email,
		Username: &username,
		Password: "Password123!",
	})
	require.NoError(t, err)
	t.Cleanup(func() { cleanupUser(db, created.ID, "") })

	assert.Equal(t, strings.ToLower(email), created.Email)
	assert.Equal(t, "Integration Tester", created.Name)
	assert.Equal(t, enums.RoleUser, created.Role)
	assert.Equal(t, enums.StatusActive, created.Status)
	require.NotNil(t, created.Username)
	assert.Equal(t, strings.ToLower(username), *created.Username)
	require.NotNil(t, created.DisplayUsername)
	assert.Equal(t, strings.ToLower(username), *created.DisplayUsername)

	matched, err := provider.VerifyPassword(ctx, created.ID, "Password123!")
	require.NoError(t, err)
	assert.True(t, matched)

	// 重复邮箱必须由唯一索引兜底并翻译为业务错误。
	_, err = service.Register(ctx, shareduser.RegisterParams{
		Name:     "Duplicate",
		Email:    email,
		Password: "Password123!",
	})
	require.Error(t, err)
	status, body := errs.Map(err)
	assert.Equal(t, 409, status)
	assert.Equal(t, string(errs.CodeAuthEmailAlreadyExists), body.Code)
}

// TestCredentialLifecycle 覆盖校验、改密、重置与缺失凭据语义。
func TestCredentialLifecycle(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	h := hasher.New()
	service := shareduser.MustNewService(db, h)
	provider := shareduser.MustNewProvider(db, h)

	created, err := service.Register(ctx, shareduser.RegisterParams{
		Name:     "Credential Tester",
		Email:    uniqueEmail(),
		Password: "Password123!",
	})
	require.NoError(t, err)
	t.Cleanup(func() { cleanupUser(db, created.ID, "") })

	t.Run("错误口令校验失败", func(t *testing.T) {
		matched, err := provider.VerifyPassword(ctx, created.ID, "WrongPass123!")
		require.NoError(t, err)
		assert.False(t, matched)
	})

	t.Run("改密需校验当前口令", func(t *testing.T) {
		err := provider.ChangePassword(ctx, created.ID, "WrongPass123!", "NewPassword456!")
		require.Error(t, err)
		_, body := errs.Map(err)
		assert.Equal(t, string(errs.CodeAuthPasswordMismatch), body.Code)
	})

	t.Run("改密后旧口令失效", func(t *testing.T) {
		require.NoError(t, provider.ChangePassword(ctx, created.ID, "Password123!", "NewPassword456!"))

		matched, err := provider.VerifyPassword(ctx, created.ID, "Password123!")
		require.NoError(t, err)
		assert.False(t, matched)

		matched, err = provider.VerifyPassword(ctx, created.ID, "NewPassword456!")
		require.NoError(t, err)
		assert.True(t, matched)
	})

	t.Run("重置口令无需当前口令", func(t *testing.T) {
		require.NoError(t, provider.ResetPassword(ctx, created.ID, "ResetPassword789!"))

		matched, err := provider.VerifyPassword(ctx, created.ID, "ResetPassword789!")
		require.NoError(t, err)
		assert.True(t, matched)
	})

	t.Run("弱口令被拒绝", func(t *testing.T) {
		err := provider.ResetPassword(ctx, created.ID, "short")
		require.Error(t, err)
		_, body := errs.Map(err)
		assert.Equal(t, string(errs.CodeAuthPasswordTooWeak), body.Code)
	})
}

// TestCredentialMissing 覆盖没有本地口令凭据时的哨兵语义。
func TestCredentialMissing(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	h := hasher.New()
	provider := shareduser.MustNewProvider(db, h)

	row, err := db.Queries(ctx).CreateUser(ctx, sqlc.CreateUserParams{
		Name:   "No Credential",
		Email:  uniqueEmail(),
		Role:   string(enums.RoleUser),
		Status: string(enums.StatusActive),
	})
	require.NoError(t, err)
	t.Cleanup(func() { cleanupUser(db, row.ID, "") })

	matched, err := provider.VerifyPassword(ctx, row.ID, "Password123!")
	require.NoError(t, err)
	assert.False(t, matched, "凭据缺失不应泄漏账号状态")

	err = provider.ChangePassword(ctx, row.ID, "Password123!", "NewPassword456!")
	require.ErrorIs(t, err, shareduser.ErrNoCredential)

	err = provider.ResetPassword(ctx, row.ID, "NewPassword456!")
	require.ErrorIs(t, err, shareduser.ErrNoCredential)
}
