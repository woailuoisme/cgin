package enums_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/shared/enums"
)

func TestRoleEnums(t *testing.T) {
	t.Parallel()

	assert.True(t, enums.RoleUser.IsValid())
	assert.True(t, enums.RoleAdmin.IsValid())
	assert.False(t, enums.Role("unknown").IsValid())

	assert.True(t, enums.RoleAdmin.IsAdmin())
	assert.False(t, enums.RoleUser.IsAdmin())

	assert.Equal(t, "user", enums.RoleUser.String())
	assert.Equal(t, "admin", enums.RoleAdmin.String())

	roles := enums.AllRoles()
	assert.Contains(t, roles, enums.RoleUser)
	assert.Contains(t, roles, enums.RoleAdmin)
}

func TestAdminRoleEnums(t *testing.T) {
	t.Parallel()

	adminRoles := enums.AdminRoleValues()
	assert.Len(t, adminRoles, 7)
	assert.Contains(t, adminRoles, enums.SuperAdmin)
	assert.Contains(t, adminRoles, enums.Admin)

	assert.True(t, enums.SuperAdmin.IsValid())
	assert.Equal(t, "super_admin", enums.SuperAdmin.String())

	role, err := enums.AdminRoleFromString("super_admin")
	require.NoError(t, err)
	assert.Equal(t, enums.SuperAdmin, role)

	role, err = enums.AdminRoleFromString("superadmin")
	require.NoError(t, err)
	assert.Equal(t, enums.SuperAdmin, role)

	assert.Equal(t, enums.SuperAdmin, enums.AdminRoleMustFromString("super_admin"))

	_, err = enums.AdminRoleFromString("invalid_role")
	require.Error(t, err)

	assert.Panics(t, func() {
		enums.AdminRoleMustFromString("invalid_role")
	})

	assert.True(t, enums.IsAdminGuardRole("super_admin"))
	assert.True(t, enums.IsAdminGuardRole("admin"))
	assert.False(t, enums.IsAdminGuardRole("user"))
}

func TestUserStatusEnums(t *testing.T) {
	t.Parallel()

	assert.True(t, enums.UserStatusActive.IsValid())
	assert.True(t, enums.UserStatusInactive.IsValid())
	assert.True(t, enums.UserStatusPending.IsValid())
	assert.True(t, enums.UserStatusSuspended.IsValid())
	assert.True(t, enums.UserStatusBanned.IsValid())
	assert.False(t, enums.UserStatus("unknown").IsValid())

	assert.True(t, enums.UserStatusActive.IsActive())
	assert.False(t, enums.UserStatusBanned.IsActive())

	assert.True(t, enums.UserStatusBanned.IsBanned())
	assert.True(t, enums.UserStatusBanned.IsBlocked())
	assert.True(t, enums.UserStatusSuspended.IsBlocked())
	assert.False(t, enums.UserStatusActive.IsBlocked())

	assert.True(t, enums.UserStatusActive.CanLogin())
	assert.True(t, enums.UserStatusPending.CanLogin())
	assert.False(t, enums.UserStatusBanned.CanLogin())

	status, err := enums.UserStatusFromString("active")
	require.NoError(t, err)
	assert.Equal(t, enums.UserStatusActive, status)

	assert.Equal(t, enums.UserStatusActive, enums.UserStatusMustFromString("active"))

	_, err = enums.UserStatusFromString("invalid_status")
	require.Error(t, err)

	assert.Panics(t, func() {
		enums.UserStatusMustFromString("invalid_status")
	})

	assert.Len(t, enums.UserStatusValues(), 5)
	assert.Len(t, enums.AllStatuses(), 3)
}

func TestOTPType(t *testing.T) {
	t.Parallel()

	values := enums.OTPTypeValues()
	assert.Len(t, values, 3)
	assert.Contains(t, values, enums.OTPTypeSignup)
	assert.Contains(t, values, enums.OTPTypePasswordReset)
	assert.Contains(t, values, enums.OTPTypeEmailVerification)

	for _, v := range values {
		assert.True(t, v.IsValid())
		assert.Equal(t, string(v), v.String())
	}

	invalid := enums.OTPType("invalid_type")
	assert.False(t, invalid.IsValid())
}

func TestProviderID(t *testing.T) {
	t.Parallel()

	assert.True(t, enums.ProviderCredential.IsValid())
	assert.True(t, enums.ProviderGoogle.IsValid())
	assert.True(t, enums.ProviderGitHub.IsValid())
	assert.True(t, enums.ProviderWeChatMiniProgram.IsValid())
	assert.False(t, enums.ProviderID("invalid").IsValid())
	assert.Equal(t, "credential", enums.ProviderCredential.String())
}
