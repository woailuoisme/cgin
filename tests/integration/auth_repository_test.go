//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/internal/shared/enums"
)

// testHash 是占位口令哈希，仓储层不校验 argon2id 本身。
const testHash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhh"

// cleanupUser 按外键顺序清理测试数据，避免污染测试库。
func cleanupUser(db *database.DB, userID, identifier string) {
	ctx := context.Background()
	if identifier != "" {
		_, _ = db.Pool().Exec(ctx, `DELETE FROM "verification" WHERE identifier = $1`, identifier)
	}
	if userID != "" {
		_, _ = db.Pool().Exec(ctx, `DELETE FROM "account" WHERE user_id = $1`, userID)
		_, _ = db.Pool().Exec(ctx, `DELETE FROM "user" WHERE id = $1`, userID)
	}
}

// createUser 通过生成查询创建用户与本地凭据。
func createUser(t *testing.T, db *database.DB, email, role, status, passwordHash string) sqlc.User {
	t.Helper()
	ctx := t.Context()

	var created sqlc.User
	err := db.RunInTx(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		row, err := q.CreateUser(ctx, sqlc.CreateUserParams{
			Name:          "Integration Tester",
			Email:         email,
			EmailVerified: false,
			Role:          role,
			Status:        status,
		})
		if err != nil {
			return err
		}
		if _, err := q.CreateAccount(ctx, sqlc.CreateAccountParams{
			UserID:     row.ID,
			AccountID:  row.ID,
			ProviderID: string(enums.ProviderCredential),
			Password:   &passwordHash,
		}); err != nil {
			return err
		}
		created = row
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { cleanupUser(db, created.ID, "") })
	return created
}

// TestRepositoryUserLifecycle 覆盖用户与凭据的完整读写路径。
func TestRepositoryUserLifecycle(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	email := uniqueEmail()

	user := createUser(t, db, email, string(enums.RoleUser), string(enums.StatusActive), testHash)

	// 主键必须是 PostgreSQL 18 生成的 UUIDv7（第 15 个字符为版本号 7）。
	require.Len(t, user.ID, 36)
	assert.Equal(t, byte('7'), user.ID[14], "应为 UUIDv7，实际 id=%s", user.ID)

	t.Run("邮箱查询大小写不敏感", func(t *testing.T) {
		found, err := db.Queries(ctx).GetUserByEmail(ctx, sqlc.GetUserByEmailParams{Email: strings.ToUpper(email)})
		require.NoError(t, err)
		assert.Equal(t, user.ID, found.ID)
	})

	t.Run("主键查询命中", func(t *testing.T) {
		found, err := db.Queries(ctx).GetUserByID(ctx, sqlc.GetUserByIDParams{ID: user.ID})
		require.NoError(t, err)
		assert.Equal(t, email, found.Email)
		assert.Equal(t, user.CreatedAt.UTC(), found.CreatedAt.UTC(), "时间戳必须按 UTC 存取")
	})

	t.Run("凭据账号可取回", func(t *testing.T) {
		account, err := db.Queries(ctx).GetCredentialAccount(ctx, sqlc.GetCredentialAccountParams{
			UserID:     user.ID,
			ProviderID: string(enums.ProviderCredential),
		})
		require.NoError(t, err)
		require.NotNil(t, account.Password)
		assert.Equal(t, testHash, *account.Password)
	})

	t.Run("不存在的用户返回 ErrNoRows", func(t *testing.T) {
		_, err := db.Queries(ctx).GetUserByEmail(ctx, sqlc.GetUserByEmailParams{Email: "nobody@cgin.local"})
		assert.ErrorIs(t, err, pgx.ErrNoRows)
	})

	t.Run("重复邮箱被唯一索引拦下", func(t *testing.T) {
		_, err := db.Queries(ctx).CreateUser(ctx, sqlc.CreateUserParams{
			Name:   "Dup",
			Email:  email,
			Role:   string(enums.RoleUser),
			Status: string(enums.StatusActive),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "idx_user_email")
	})

	t.Run("改写口令影响一行", func(t *testing.T) {
		newHash := testHash + "x"
		affected, err := db.Queries(ctx).UpdateAccountPassword(ctx, sqlc.UpdateAccountPasswordParams{
			UserID:     user.ID,
			ProviderID: string(enums.ProviderCredential),
			Password:   &newHash,
		})
		require.NoError(t, err)
		assert.EqualValues(t, 1, affected)

		missing := testHash
		affected, err = db.Queries(ctx).UpdateAccountPassword(ctx, sqlc.UpdateAccountPasswordParams{
			UserID:     "u_missing",
			ProviderID: string(enums.ProviderCredential),
			Password:   &missing,
		})
		require.NoError(t, err)
		assert.Zero(t, affected)
	})

	t.Run("记录登录与邮箱验证", func(t *testing.T) {
		method := string(enums.ProviderCredential)
		require.NoError(t, db.Queries(ctx).RecordUserLogin(ctx, sqlc.RecordUserLoginParams{
			ID:     user.ID,
			Method: &method,
		}))
		_, err := db.Queries(ctx).SetUserEmailVerified(ctx, sqlc.SetUserEmailVerifiedParams{ID: user.ID})
		require.NoError(t, err)

		found, err := db.Queries(ctx).GetUserByID(ctx, sqlc.GetUserByIDParams{ID: user.ID})
		require.NoError(t, err)
		assert.True(t, found.EmailVerified)
		require.NotNil(t, found.LastLoginMethod)
		assert.Equal(t, method, *found.LastLoginMethod)
		require.NotNil(t, found.LastLoginAt)
	})

	t.Run("用户计数不为零", func(t *testing.T) {
		count, err := db.Queries(ctx).CountActiveUsers(ctx)
		require.NoError(t, err)
		assert.Positive(t, count)
	})
}

// TestRepositoryVerificationLifecycle 覆盖一次性凭据的写入、覆盖与核销。
func TestRepositoryVerificationLifecycle(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()

	identifier := "email-verify:" + uniqueEmail()
	t.Cleanup(func() { cleanupUser(db, "", identifier) })

	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	_, err := db.Queries(ctx).UpsertVerification(ctx, sqlc.UpsertVerificationParams{
		Identifier: identifier,
		Value:      "digest-1",
		ExpiresAt:  expiresAt,
	})
	require.NoError(t, err)

	record, err := db.Queries(ctx).GetVerificationByIdentifier(ctx, sqlc.GetVerificationByIdentifierParams{Identifier: identifier})
	require.NoError(t, err)
	assert.Equal(t, "digest-1", record.Value)
	assert.True(t, record.ExpiresAt.UTC().Equal(expiresAt), "过期时间必须按 UTC 存取")

	// 同 identifier 再次写入必须覆盖，保证一个主体只有一条待核销凭据。
	_, err = db.Queries(ctx).UpsertVerification(ctx, sqlc.UpsertVerificationParams{
		Identifier: identifier,
		Value:      "digest-2",
		ExpiresAt:  expiresAt,
	})
	require.NoError(t, err)
	again, err := db.Queries(ctx).GetVerificationByIdentifier(ctx, sqlc.GetVerificationByIdentifierParams{Identifier: identifier})
	require.NoError(t, err)
	assert.Equal(t, "digest-2", again.Value)

	// 摘要不匹配时核销必须落空。
	consumed, err := db.Queries(ctx).ConsumeVerification(ctx, sqlc.ConsumeVerificationParams{
		Identifier: identifier,
		Value:      "digest-1",
	})
	require.NoError(t, err)
	assert.Zero(t, consumed)

	consumed, err = db.Queries(ctx).ConsumeVerification(ctx, sqlc.ConsumeVerificationParams{
		Identifier: identifier,
		Value:      "digest-2",
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, consumed)

	_, err = db.Queries(ctx).GetVerificationByIdentifier(ctx, sqlc.GetVerificationByIdentifierParams{Identifier: identifier})
	assert.ErrorIs(t, err, pgx.ErrNoRows)
}

// TestSoftDeleteFreesEmail 校验软删后邮箱可被重新注册。
// 这依赖 schema 里 idx_user_email 的 WHERE deleted_at IS NULL 部分唯一索引。
func TestSoftDeleteFreesEmail(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	email := uniqueEmail()

	first := createUser(t, db, email, string(enums.RoleUser), string(enums.StatusActive), testHash)

	affected, err := db.Queries(ctx).SoftDeleteUser(ctx, sqlc.SoftDeleteUserParams{ID: first.ID})
	require.NoError(t, err)
	assert.EqualValues(t, 1, affected)

	// 软删后原邮箱对业务查询不可见。
	_, err = db.Queries(ctx).GetUserByEmail(ctx, sqlc.GetUserByEmailParams{Email: email})
	require.ErrorIs(t, err, pgx.ErrNoRows, "软删用户不应出现在业务查询里")

	// 且同一邮箱可以重新注册，这正是部分唯一索引的设计意图。
	second := createUser(t, db, email, string(enums.RoleUser), string(enums.StatusActive), testHash)
	assert.NotEqual(t, first.ID, second.ID)
}

// TestSchemaEnforcesRoleAndStatusChecks 校验库层的 CHECK 约束确实生效（ADR-0004）。
func TestSchemaEnforcesRoleAndStatusChecks(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()

	t.Run("非法角色被拒绝", func(t *testing.T) {
		_, err := db.Queries(ctx).CreateUser(ctx, sqlc.CreateUserParams{
			Name:   "Bad Role",
			Email:  uniqueEmail(),
			Role:   "editor",
			Status: string(enums.StatusActive),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ck_user_role")
	})

	t.Run("非法状态被拒绝", func(t *testing.T) {
		_, err := db.Queries(ctx).CreateUser(ctx, sqlc.CreateUserParams{
			Name:   "Bad Status",
			Email:  uniqueEmail(),
			Role:   string(enums.RoleUser),
			Status: "sleeping",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ck_user_status")
	})
}
