package enum_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/enum"
)

func TestRoleEnums(t *testing.T) {
	t.Parallel()

	assert.True(t, enum.RoleUser.IsValid())
	assert.True(t, enum.RoleAdmin.IsValid())
	assert.False(t, enum.Role("unknown").IsValid())

	assert.True(t, enum.RoleAdmin.IsAdmin())
	assert.False(t, enum.RoleUser.IsAdmin())

	assert.Equal(t, "user", enum.RoleUser.String())
	assert.Equal(t, "admin", enum.RoleAdmin.String())

	roles := enum.AllRoles()
	assert.Contains(t, roles, enum.RoleUser)
	assert.Contains(t, roles, enum.RoleAdmin)
}

func TestAdminRoleEnums(t *testing.T) {
	t.Parallel()

	adminRoles := enum.AdminRoleValues()
	assert.Len(t, adminRoles, 7)
	assert.Contains(t, adminRoles, enum.SuperAdmin)
	assert.Contains(t, adminRoles, enum.Admin)

	assert.True(t, enum.SuperAdmin.IsValid())
	assert.Equal(t, "super_admin", enum.SuperAdmin.String())

	role, err := enum.AdminRoleFromString("super_admin")
	require.NoError(t, err)
	assert.Equal(t, enum.SuperAdmin, role)

	role, err = enum.AdminRoleFromString("superadmin")
	require.NoError(t, err)
	assert.Equal(t, enum.SuperAdmin, role)

	assert.Equal(t, enum.SuperAdmin, enum.AdminRoleMustFromString("super_admin"))

	_, err = enum.AdminRoleFromString("invalid_role")
	require.Error(t, err)

	assert.Panics(t, func() {
		enum.AdminRoleMustFromString("invalid_role")
	})

	assert.True(t, enum.IsAdminGuardRole("super_admin"))
	assert.True(t, enum.IsAdminGuardRole("admin"))
	assert.False(t, enum.IsAdminGuardRole("user"))
}

func TestUserStatusEnums(t *testing.T) {
	t.Parallel()

	assert.True(t, enum.UserStatusActive.IsValid())
	assert.True(t, enum.UserStatusInactive.IsValid())
	assert.True(t, enum.UserStatusPending.IsValid())
	assert.True(t, enum.UserStatusSuspended.IsValid())
	assert.True(t, enum.UserStatusBanned.IsValid())
	assert.False(t, enum.UserStatus("unknown").IsValid())

	assert.True(t, enum.UserStatusActive.IsActive())
	assert.False(t, enum.UserStatusBanned.IsActive())

	assert.True(t, enum.UserStatusBanned.IsBanned())
	assert.True(t, enum.UserStatusBanned.IsBlocked())
	assert.True(t, enum.UserStatusSuspended.IsBlocked())
	assert.False(t, enum.UserStatusActive.IsBlocked())

	assert.True(t, enum.UserStatusActive.CanLogin())
	assert.True(t, enum.UserStatusPending.CanLogin())
	assert.False(t, enum.UserStatusBanned.CanLogin())

	status, err := enum.UserStatusFromString("active")
	require.NoError(t, err)
	assert.Equal(t, enum.UserStatusActive, status)

	assert.Equal(t, enum.UserStatusActive, enum.UserStatusMustFromString("active"))

	_, err = enum.UserStatusFromString("invalid_status")
	require.Error(t, err)

	assert.Panics(t, func() {
		enum.UserStatusMustFromString("invalid_status")
	})

	assert.Len(t, enum.UserStatusValues(), 5)
	assert.Len(t, enum.AllStatuses(), 3)
}

func TestOTPType(t *testing.T) {
	t.Parallel()

	values := enum.OTPTypeValues()
	assert.Len(t, values, 3)
	assert.Contains(t, values, enum.OTPTypeSignup)
	assert.Contains(t, values, enum.OTPTypePasswordReset)
	assert.Contains(t, values, enum.OTPTypeEmailVerification)

	for _, v := range values {
		assert.True(t, v.IsValid())
		assert.Equal(t, string(v), v.String())
	}

	invalid := enum.OTPType("invalid_type")
	assert.False(t, invalid.IsValid())
}

func TestProviderID(t *testing.T) {
	t.Parallel()

	assert.True(t, enum.ProviderCredential.IsValid())
	assert.True(t, enum.ProviderGoogle.IsValid())
	assert.True(t, enum.ProviderGitHub.IsValid())
	assert.True(t, enum.ProviderWeChatMiniProgram.IsValid())
	assert.False(t, enum.ProviderID("invalid").IsValid())
	assert.Equal(t, "credential", enum.ProviderCredential.String())
}

func TestEnumSerializationAndSQL(t *testing.T) {
	t.Parallel()

	t.Run("Role Text and SQL", func(t *testing.T) {
		r := enum.RoleUser
		data, err := json.Marshal(r)
		require.NoError(t, err)
		assert.Equal(t, `"user"`, string(data))

		var unmarshaled enum.Role
		err = json.Unmarshal([]byte(`"admin"`), &unmarshaled)
		require.NoError(t, err)
		assert.Equal(t, enum.RoleAdmin, unmarshaled)

		err = json.Unmarshal([]byte(`"invalid_role"`), &unmarshaled)
		require.Error(t, err)

		val, err := enum.RoleUser.Value()
		require.NoError(t, err)
		assert.Equal(t, "user", val)

		_, err = enum.Role("bad").Value()
		require.Error(t, err)

		var scanned enum.Role
		err = scanned.Scan("admin")
		require.NoError(t, err)
		assert.Equal(t, enum.RoleAdmin, scanned)

		err = scanned.Scan([]byte("user"))
		require.NoError(t, err)
		assert.Equal(t, enum.RoleUser, scanned)

		err = scanned.Scan("bad_role")
		require.Error(t, err)

		err = scanned.Scan(nil)
		require.NoError(t, err)
		assert.Empty(t, scanned)
	})

	t.Run("AdminRole Text and SQL", func(t *testing.T) {
		r := enum.SuperAdmin
		data, err := json.Marshal(r)
		require.NoError(t, err)
		assert.Equal(t, `"super_admin"`, string(data))

		var unmarshaled enum.AdminRole
		err = json.Unmarshal([]byte(`"admin"`), &unmarshaled)
		require.NoError(t, err)
		assert.Equal(t, enum.Admin, unmarshaled)

		err = json.Unmarshal([]byte(`"bad"`), &unmarshaled)
		require.Error(t, err)

		val, err := enum.SuperAdmin.Value()
		require.NoError(t, err)
		assert.Equal(t, "super_admin", val)

		var scanned enum.AdminRole
		err = scanned.Scan("editor")
		require.NoError(t, err)
		assert.Equal(t, enum.Editor, scanned)
	})

	t.Run("UserStatus Text and SQL", func(t *testing.T) {
		s := enum.UserStatusActive
		data, err := json.Marshal(s)
		require.NoError(t, err)
		assert.Equal(t, `"active"`, string(data))

		var unmarshaled enum.UserStatus
		err = json.Unmarshal([]byte(`"banned"`), &unmarshaled)
		require.NoError(t, err)
		assert.Equal(t, enum.UserStatusBanned, unmarshaled)

		err = json.Unmarshal([]byte(`"bad"`), &unmarshaled)
		require.Error(t, err)

		val, err := enum.UserStatusActive.Value()
		require.NoError(t, err)
		assert.Equal(t, "active", val)

		var scanned enum.UserStatus
		err = scanned.Scan("inactive")
		require.NoError(t, err)
		assert.Equal(t, enum.UserStatusInactive, scanned)
	})

	t.Run("OTPType Text and SQL", func(t *testing.T) {
		otp := enum.OTPTypeSignup
		data, err := json.Marshal(otp)
		require.NoError(t, err)
		assert.Equal(t, `"signup"`, string(data))

		var unmarshaled enum.OTPType
		err = json.Unmarshal([]byte(`"password_reset"`), &unmarshaled)
		require.NoError(t, err)
		assert.Equal(t, enum.OTPTypePasswordReset, unmarshaled)

		err = json.Unmarshal([]byte(`"bad"`), &unmarshaled)
		require.Error(t, err)

		val, err := enum.OTPTypeSignup.Value()
		require.NoError(t, err)
		assert.Equal(t, "signup", val)

		var scanned enum.OTPType
		err = scanned.Scan("email_verification")
		require.NoError(t, err)
		assert.Equal(t, enum.OTPTypeEmailVerification, scanned)
	})

	t.Run("ProviderID Text and SQL", func(t *testing.T) {
		p := enum.ProviderGitHub
		data, err := json.Marshal(p)
		require.NoError(t, err)
		assert.Equal(t, `"github"`, string(data))

		var unmarshaled enum.ProviderID
		err = json.Unmarshal([]byte(`"google"`), &unmarshaled)
		require.NoError(t, err)
		assert.Equal(t, enum.ProviderGoogle, unmarshaled)

		err = json.Unmarshal([]byte(`"bad"`), &unmarshaled)
		require.Error(t, err)

		val, err := enum.ProviderGitHub.Value()
		require.NoError(t, err)
		assert.Equal(t, "github", val)

		var scanned enum.ProviderID
		err = scanned.Scan("credential")
		require.NoError(t, err)
		assert.Equal(t, enum.ProviderCredential, scanned)
	})
}
