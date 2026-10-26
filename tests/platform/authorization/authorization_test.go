package authorization_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/pkg/authentication"
	"cgin/pkg/authorization"
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

// TestEnforcer_PathRuleBoundary 锁定 matcher 双语义的边界。
//
// matcher 同时服务资源级鉴权（裸资源名）与路径级鉴权（请求路径 + HTTP 方法）：
// 路径通配规则（如 /v1/admin/*）会让 URL 形态的 resource 名在资源级调用中被放行，
// 因此 Can() 的调用方必须始终传裸资源名——这里把该边界固化为断言，防止策略演进时悄然漂移。
func TestEnforcer_PathRuleBoundary(t *testing.T) {
	e, err := authorization.New()
	require.NoError(t, err)

	tests := []struct {
		name     string
		role     string
		resource string
		action   string
		expected bool
	}{
		{"admin 命中自身路径通配", "admin", "/v1/admin/users", "GET", true},
		{"admin 通配覆盖子路径", "admin", "/v1/admin/users/1", "DELETE", true},
		{"client 路径对 admin 不放行", "admin", "/v1/users/me", "PATCH", false},
		{"client 不得进入 admin 通配", "client", "/v1/admin/users", "GET", false},
		{"client 仅限白名单方法", "client", "/v1/users/me", "DELETE", false},
		{"既有资源语义不受影响", "admin", authorization.ResourceUsers, authorization.ActionDelete, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
