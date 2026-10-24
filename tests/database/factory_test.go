package database_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/database/factories"
	"bgin/internal/database/models"
)

func TestFactory_InMemoryMake(t *testing.T) {
	ctx := t.Context()

	t.Run("Make default user without database", func(t *testing.T) {
		u, err := factories.User(nil).Make(ctx)
		require.NoError(t, err)
		require.NotNil(t, u)

		assert.NotEmpty(t, u.ID)
		assert.NotEmpty(t, u.Name)
		assert.Contains(t, u.Email, "@")
		assert.True(t, u.EmailVerified)
		assert.Equal(t, models.RoleUser, u.Role)
		assert.Equal(t, models.StatusActive, u.Status)
		assert.False(t, u.Banned)
		assert.False(t, u.TwoFactorEnabled)
		assert.NotNil(t, u.PhoneNumber)
		assert.NotZero(t, u.CreatedAt)
		assert.NotZero(t, u.UpdatedAt)
	})

	t.Run("Make default account without database", func(t *testing.T) {
		acc, err := factories.Account(nil).Make(ctx)
		require.NoError(t, err)
		require.NotNil(t, acc)

		assert.NotEmpty(t, acc.ID)
		assert.NotEmpty(t, acc.UserID)
		assert.NotEmpty(t, acc.AccountID)
		assert.Equal(t, models.ProviderCredential, acc.ProviderID)
		assert.NotNil(t, acc.Password)
	})
}

func TestFactory_Immutability(t *testing.T) {
	ctx := t.Context()
	base := factories.User(nil)

	adminFactory := base.Admin()
	bannedReason := "Account suspended"
	now := time.Now().UTC()
	bannedFactory := base.Banned(bannedReason, &now)
	unverifiedFactory := base.Unverified()

	// 原始 base 实例保持默认普通用户属性
	defaultUser, err := base.Make(ctx)
	require.NoError(t, err)
	assert.Equal(t, models.RoleUser, defaultUser.Role)
	assert.Equal(t, models.StatusActive, defaultUser.Status)
	assert.True(t, defaultUser.EmailVerified)

	// admin 副本
	adminUser, err := adminFactory.Make(ctx)
	require.NoError(t, err)
	assert.Equal(t, models.RoleAdmin, adminUser.Role)
	assert.Equal(t, models.StatusActive, adminUser.Status)

	// banned 副本
	bannedUser, err := bannedFactory.Make(ctx)
	require.NoError(t, err)
	assert.Equal(t, models.StatusBanned, bannedUser.Status)
	assert.True(t, bannedUser.Banned)
	assert.Equal(t, &bannedReason, bannedUser.BanReason)

	// unverified 副本
	unverifiedUser, err := unverifiedFactory.Make(ctx)
	require.NoError(t, err)
	assert.False(t, unverifiedUser.EmailVerified)
	assert.False(t, unverifiedUser.PhoneNumberVerified)
}

func TestFactory_MakeManyAndSequence(t *testing.T) {
	ctx := t.Context()

	t.Run("Count and MakeMany", func(t *testing.T) {
		users, err := factories.User(nil).Count(5).MakeMany(ctx)
		require.NoError(t, err)
		assert.Len(t, users, 5)

		// 确保每个实例都是唯一的
		emails := make(map[string]bool)
		for _, u := range users {
			assert.NotEmpty(t, u.ID)
			emails[u.Email] = true
		}
		assert.Len(t, emails, 5)
	})

	t.Run("Sequence modifier", func(t *testing.T) {
		users, err := factories.User(nil).
			Sequence(func(u *models.User, index int) {
				u.Email = fmt.Sprintf("seq_%d@example.com", index)
			}).
			MakeMany(ctx, 3)

		require.NoError(t, err)
		require.Len(t, users, 3)
		assert.Equal(t, "seq_0@example.com", users[0].Email)
		assert.Equal(t, "seq_1@example.com", users[1].Email)
		assert.Equal(t, "seq_2@example.com", users[2].Email)
	})
}

func TestFactory_DeterministicSeed(t *testing.T) {
	ctx := t.Context()

	f1 := factories.User(nil).Seed(42)
	u1, err := f1.Make(ctx)
	require.NoError(t, err)

	f2 := factories.User(nil).Seed(42)
	u2, err := f2.Make(ctx)
	require.NoError(t, err)

	// 相同种子应生成相同的值（姓名、邮箱、用户名等由 Faker 决定）
	assert.Equal(t, u1.Name, u2.Name)
	assert.Equal(t, u1.Email, u2.Email)
}

func TestFactory_AccountModifiers(t *testing.T) {
	ctx := t.Context()

	t.Run("ForUser and Password", func(t *testing.T) {
		acc, err := factories.Account(nil).
			ForUser("user_123", "alice@example.com").
			Password("CustomPass!").
			Make(ctx)

		require.NoError(t, err)
		assert.Equal(t, "user_123", acc.UserID)
		assert.Equal(t, "alice@example.com", acc.AccountID)
		assert.NotNil(t, acc.Password)
	})

	t.Run("OAuth provider", func(t *testing.T) {
		acc, err := factories.Account(nil).
			OAuth("github", "gh_9999").
			Make(ctx)

		require.NoError(t, err)
		assert.Equal(t, "github", acc.ProviderID)
		assert.Equal(t, "gh_9999", acc.AccountID)
		assert.Nil(t, acc.Password)
		assert.NotNil(t, acc.AccessToken)
	})
}

func TestFactory_RequireDatabaseError(t *testing.T) {
	ctx := t.Context()

	_, err := factories.User(nil).Create(ctx)
	require.ErrorIs(t, err, factories.ErrDatabaseRequired)

	_, err = factories.User(nil).CreateMany(ctx, 3)
	require.ErrorIs(t, err, factories.ErrDatabaseRequired)
}
