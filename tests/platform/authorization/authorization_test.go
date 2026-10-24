package authorization_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/authentication"
	"bgin/internal/platform/authorization"
)

func TestEnforcer_CanRole(t *testing.T) {
	e, err := authorization.New()
	require.NoError(t, err)
	require.NotNil(t, e)

	tests := []struct {
		role     string
		resource string
		action   string
		expected bool
	}{
		{"admin", authorization.ResourceUsers, authorization.ActionCreate, true},
		{"admin", authorization.ResourceUsers, authorization.ActionList, true},
		{"admin", authorization.ResourceUsers, authorization.ActionRead, true},
		{"admin", authorization.ResourceUsers, authorization.ActionUpdate, true},
		{"admin", authorization.ResourceUsers, authorization.ActionDelete, true},
		{"user", authorization.ResourceUsers, authorization.ActionDelete, false},
		{"guest", authorization.ResourceUsers, authorization.ActionRead, false},
		{"", authorization.ResourceUsers, authorization.ActionList, false},
	}

	for _, tt := range tests {
		t.Run(tt.role+":"+tt.resource+":"+tt.action, func(t *testing.T) {
			allowed, err := e.CanRole(tt.role, tt.resource, tt.action)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, allowed)
		})
	}
}

func TestEnforcer_CanRoles(t *testing.T) {
	e, err := authorization.New()
	require.NoError(t, err)

	assert.True(t, e.CanRoles([]string{"user", "admin"}, authorization.ResourceUsers, authorization.ActionCreate))
	assert.False(t, e.CanRoles([]string{"user", "guest"}, authorization.ResourceUsers, authorization.ActionDelete))
	assert.False(t, e.CanRoles(nil, authorization.ResourceUsers, authorization.ActionList))
}

func TestEnforcer_GateContract(t *testing.T) {
	e, err := authorization.New()
	require.NoError(t, err)

	ctx := context.Background()

	// 1. 未认证上下文
	assert.False(t, e.Can(ctx, authorization.ResourceUsers, authorization.ActionRead))
	assert.False(t, e.Allows(ctx, authorization.ResourceUsers, authorization.ActionRead))
	assert.True(t, e.Denies(ctx, authorization.ResourceUsers, authorization.ActionRead))
	err = e.Authorize(ctx, authorization.ResourceUsers, authorization.ActionRead)
	require.Error(t, err)

	// 2. 普通用户角色上下文
	userCtx := authentication.With(ctx, authentication.Identity{
		UserID: "user-1",
		Role:   "user",
	})
	assert.False(t, e.Can(userCtx, authorization.ResourceUsers, authorization.ActionDelete))
	assert.True(t, e.Denies(userCtx, authorization.ResourceUsers, authorization.ActionDelete))
	require.Error(t, e.Authorize(userCtx, authorization.ResourceUsers, authorization.ActionDelete))

	// 3. 管理员角色上下文
	adminCtx := authentication.With(ctx, authentication.Identity{
		UserID: "admin-1",
		Role:   "admin",
	})
	assert.True(t, e.Can(adminCtx, authorization.ResourceUsers, authorization.ActionDelete))
	assert.True(t, e.Allows(adminCtx, authorization.ResourceUsers, authorization.ActionDelete))
	assert.False(t, e.Denies(adminCtx, authorization.ResourceUsers, authorization.ActionDelete))
	require.NoError(t, e.Authorize(adminCtx, authorization.ResourceUsers, authorization.ActionDelete))

	// 4. 多角色上下文（Roles 切片）
	multiCtx := authentication.With(ctx, authentication.Identity{
		UserID: "multi-1",
		Roles:  []string{"editor", "admin"},
	})
	assert.True(t, e.Can(multiCtx, authorization.ResourceUsers, authorization.ActionCreate))
	require.NoError(t, e.Authorize(multiCtx, authorization.ResourceUsers, authorization.ActionCreate))
}
