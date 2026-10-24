package testutil_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/database/models"
	"bgin/internal/shared/enums"
	"bgin/tests/testutil"
)

func TestUserFixtureBuilder(t *testing.T) {
	t.Parallel()

	t.Run("default user has valid state", func(t *testing.T) {
		u := testutil.User().Build()
		require.NotNil(t, u)
		assert.NotEmpty(t, u.ID)
		assert.NotEmpty(t, u.Email)
		assert.Equal(t, enums.RoleUser, u.Role)
		assert.Equal(t, enums.StatusActive, u.Status)
		assert.True(t, u.EmailVerified)
		assert.False(t, u.Banned)
		assert.False(t, u.TwoFactorEnabled)
	})

	t.Run("admin builder sets admin role", func(t *testing.T) {
		u := testutil.User().Admin().Build()
		assert.Equal(t, enums.RoleAdmin, u.Role)
	})

	t.Run("inactive builder sets inactive status", func(t *testing.T) {
		u := testutil.User().Inactive().Build()
		assert.Equal(t, enums.StatusInactive, u.Status)
	})

	t.Run("unverified builder resets verification flags", func(t *testing.T) {
		u := testutil.User().Unverified().Build()
		assert.False(t, u.EmailVerified)
		assert.False(t, u.PhoneNumberVerified)
	})

	t.Run("banned builder sets banned flags and expiry", func(t *testing.T) {
		future := time.Now().UTC().Add(time.Hour)
		u := testutil.User().Banned("spamming", future).Build()
		assert.True(t, u.Banned)
		assert.Equal(t, enums.StatusBanned, u.Status)
		require.NotNil(t, u.BanReason)
		assert.Equal(t, "spamming", *u.BanReason)
		require.NotNil(t, u.BanExpires)
		assert.Equal(t, future.Unix(), u.BanExpires.Unix())
	})

	t.Run("two factor builder toggles state", func(t *testing.T) {
		u1 := testutil.User().TwoFactor().Build()
		assert.True(t, u1.TwoFactorEnabled)

		u2 := testutil.User().TwoFactor(false).Build()
		assert.False(t, u2.TwoFactorEnabled)
	})

	t.Run("deleted builder sets soft delete timestamp", func(t *testing.T) {
		u := testutil.User().Deleted().Build()
		require.NotNil(t, u.DeletedAt)
	})

	t.Run("custom fields and build value", func(t *testing.T) {
		val := testutil.User().
			WithID("custom-id").
			WithName("Custom Name").
			WithEmail("custom@example.com").
			WithPhone("+8613900000000").
			BuildValue()

		assert.Equal(t, "custom-id", val.ID)
		assert.Equal(t, "Custom Name", val.Name)
		assert.Equal(t, "custom@example.com", val.Email)
		require.NotNil(t, val.PhoneNumber)
		assert.Equal(t, "+8613900000000", *val.PhoneNumber)
	})
}

func TestAccountFixtureBuilder(t *testing.T) {
	t.Parallel()

	t.Run("default account has valid state", func(t *testing.T) {
		acc := testutil.Account().Build()
		require.NotNil(t, acc)
		assert.NotEmpty(t, acc.ID)
		assert.NotEmpty(t, acc.UserID)
		assert.NotEmpty(t, acc.AccountID)
		assert.Equal(t, models.ProviderCredential, acc.ProviderID)
		assert.NotNil(t, acc.Password)
	})

	t.Run("custom fields and build value", func(t *testing.T) {
		val := testutil.Account().
			WithID("acc-123").
			WithUserID("usr-456").
			WithProvider("google").
			WithAccountID("google-sub-789").
			WithPassword("hashed-pw").
			BuildValue()

		assert.Equal(t, "acc-123", val.ID)
		assert.Equal(t, "usr-456", val.UserID)
		assert.Equal(t, "google", val.ProviderID)
		assert.Equal(t, "google-sub-789", val.AccountID)
		require.NotNil(t, val.Password)
		assert.Equal(t, "hashed-pw", *val.Password)
	})
}
