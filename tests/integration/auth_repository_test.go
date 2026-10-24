//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"bgin/internal/database/models"
)

// testHash 是占位口令哈希，模型层不校验 argon2id 本身。
const testHash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhh"

// cleanupUser 按外键顺序清理测试数据，避免污染测试库。
func cleanupUser(db *bun.DB, userID, identifier string) {
	ctx := context.Background()
	if identifier != "" {
		_, _ = db.NewDelete().Model((*models.Verification)(nil)).Where("identifier = ?", identifier).Exec(ctx)
	}
	if userID != "" {
		_, _ = db.NewDelete().Model((*models.Account)(nil)).Where("user_id = ?", userID).Exec(ctx)
		_, _ = db.NewDelete().Model((*models.User)(nil)).Where("id = ?", userID).Exec(ctx)
	}
}

// TestRepositoryUserLifecycle 覆盖用户与凭据的完整读写路径。
func TestRepositoryUserLifecycle(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	email := uniqueEmail()

	user := &models.User{
		Name:   "Integration Tester",
		Email:  email,
		Role:   models.RoleUser,
		Status: models.StatusActive,
	}
	require.NoError(t, models.CreateUserWithCredential(ctx, db, user, testHash))
	require.NotEmpty(t, user.ID)
	t.Cleanup(func() { cleanupUser(db, user.ID, "") })

	// 主键必须是 Go 侧生成的 UUIDv7（第 15 个字符为版本号 7）。
	assert.Equal(t, byte('7'), user.ID[14], "应为 UUIDv7，实际 id=%s", user.ID)

	t.Run("邮箱查询大小写不敏感", func(t *testing.T) {
		found, err := models.FindUserByEmail(ctx, db, strings.ToUpper(email))
		require.NoError(t, err)
		assert.Equal(t, user.ID, found.ID)
	})

	t.Run("主键查询命中", func(t *testing.T) {
		found, err := models.FindUserByID(ctx, db, user.ID)
		require.NoError(t, err)
		assert.Equal(t, email, found.Email)
		assert.Equal(t, user.CreatedAt.UTC(), found.CreatedAt.UTC(), "时间戳必须按 UTC 存取")
	})

	t.Run("凭据账号可取回", func(t *testing.T) {
		account, err := models.FindCredentialAccount(ctx, db, user.ID)
		require.NoError(t, err)
		require.NotNil(t, account.Password)
		assert.Equal(t, testHash, *account.Password)
	})

	t.Run("不存在的用户返回 ErrNoRows", func(t *testing.T) {
		_, err := models.FindUserByEmail(ctx, db, "nobody@bgin.local")
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("重复邮箱被唯一索引拦下", func(t *testing.T) {
		duplicate := &models.User{Name: "Dup", Email: email, Role: models.RoleUser, Status: models.StatusActive}
		err := models.CreateUserWithCredential(ctx, db, duplicate, testHash)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "idx_user_email")
	})

	t.Run("改写口令影响一行", func(t *testing.T) {
		require.NoError(t, models.UpdatePassword(ctx, db, user.ID, testHash+"x"))
		assert.ErrorIs(t, models.UpdatePassword(ctx, db, "u_missing", testHash), sql.ErrNoRows)
	})

	t.Run("记录登录与邮箱验证", func(t *testing.T) {
		require.NoError(t, user.RecordLogin(ctx, db, models.ProviderCredential))
		require.NoError(t, user.MarkEmailVerified(ctx, db))

		found, err := models.FindUserByID(ctx, db, user.ID)
		require.NoError(t, err)
		assert.True(t, found.EmailVerified)
		require.NotNil(t, found.LastLoginMethod)
		assert.Equal(t, models.ProviderCredential, *found.LastLoginMethod)
		require.NotNil(t, found.LastLoginAt)
	})

	t.Run("用户计数不为零", func(t *testing.T) {
		count, err := models.CountActiveUsers(ctx, db)
		require.NoError(t, err)
		assert.Positive(t, count)
	})
}

// TestRepositoryVerificationLifecycle 覆盖一次性凭据的写入、读取与核销。
func TestRepositoryVerificationLifecycle(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()

	identifier := "email-verify:" + uniqueEmail()
	t.Cleanup(func() { cleanupUser(db, "", identifier) })

	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	require.NoError(t, models.SaveVerification(ctx, db, identifier, "digest-1", expiresAt))

	record, err := models.FindVerification(ctx, db, identifier)
	require.NoError(t, err)
	assert.Equal(t, "digest-1", record.Value)
	assert.True(t, record.ExpiresAt.UTC().Equal(expiresAt), "过期时间必须按 UTC 存取")

	// 同 identifier 再次写入必须覆盖，保证一个主体只有一条待核销凭据。
	require.NoError(t, models.SaveVerification(ctx, db, identifier, "digest-2", expiresAt))
	again, err := models.FindVerification(ctx, db, identifier)
	require.NoError(t, err)
	assert.Equal(t, "digest-2", again.Value)

	require.NoError(t, models.DeleteVerification(ctx, db, again.ID))
	_, err = models.FindVerification(ctx, db, identifier)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

// TestSoftDeleteFreesEmail 校验软删后邮箱可被重新注册。
// 这依赖 schema 里 idx_user_email 的 WHERE deleted_at IS NULL 部分唯一索引。
func TestSoftDeleteFreesEmail(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	email := uniqueEmail()

	first := &models.User{Name: "First", Email: email, Role: models.RoleUser, Status: models.StatusActive}
	require.NoError(t, models.CreateUserWithCredential(ctx, db, first, testHash))
	t.Cleanup(func() { cleanupUser(db, first.ID, "") })

	now := time.Now().UTC()
	_, err := db.NewUpdate().
		Model((*models.User)(nil)).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("id = ?", first.ID).
		Exec(ctx)
	require.NoError(t, err)

	// 软删后原邮箱对业务查询不可见。
	_, err = models.FindUserByEmail(ctx, db, email)
	require.ErrorIs(t, err, sql.ErrNoRows, "软删用户不应出现在业务查询里")

	// 且同一邮箱可以重新注册，这正是部分唯一索引的设计意图。
	second := &models.User{Name: "Second", Email: email, Role: models.RoleUser, Status: models.StatusActive}
	require.NoError(t, models.CreateUserWithCredential(ctx, db, second, testHash))
	t.Cleanup(func() { cleanupUser(db, second.ID, "") })
	assert.NotEqual(t, first.ID, second.ID)
}

// TestSchemaEnforcesRoleAndStatusChecks 校验库层的 CHECK 约束确实生效（ADR-0004）。
func TestSchemaEnforcesRoleAndStatusChecks(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()

	t.Run("非法角色被拒绝", func(t *testing.T) {
		user := &models.User{Name: "Bad Role", Email: uniqueEmail(), Role: "editor", Status: models.StatusActive}
		err := models.CreateUserWithCredential(ctx, db, user, testHash)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ck_user_role")
		cleanupUser(db, user.ID, "")
	})

	t.Run("非法状态被拒绝", func(t *testing.T) {
		user := &models.User{Name: "Bad Status", Email: uniqueEmail(), Role: models.RoleUser, Status: "sleeping"}
		err := models.CreateUserWithCredential(ctx, db, user, testHash)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ck_user_status")
		cleanupUser(db, user.ID, "")
	})
}
