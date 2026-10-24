// Package apidocs_test 把"哪些端点进前台文档、哪些进管理端文档"固化成回归测试。
//
// 规范本身由 `just docs` 生成，分别输出前台客户端规范与后台管理端规范。
// 这里严格校验两份文档的边界：
// 1. 前台客户端规范收录自助与认证端点，严格排除所有后台管理端点；
// 2. 后台管理端规范收录管理端点，严格排除前台自助端点。
package apidocs_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/shared/apidocs"
)

// TestClientSpecCoversClientSurfaceOnly 校验客户端规范的收录范围与隔离性。
func TestClientSpecCoversClientSurfaceOnly(t *testing.T) {
	var spec struct {
		BasePath string                     `json:"basePath"`
		Paths    map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(apidocs.Spec, &spec), "客户端规范无法解析，可能需要重新执行 just docs")
	require.NotEmpty(t, spec.Paths)

	assert.Equal(t, "/v1", spec.BasePath)

	// 前端自助与认证端点必须在客户端文档里；路径不含 /v1 前缀，因为 basePath 已经是它。
	for _, path := range []string{"/users/me", "/auth/sign-in/email", "/auth/token/refresh"} {
		assert.Contains(t, spec.Paths, path)
	}

	// 客户端文档严格排除后台管理端点。
	for path := range spec.Paths {
		assert.NotContains(t, path, "/admin/", "后台管理端点不应出现在前台客户端文档里")
	}
}

// TestAdminSpecCoversAdminSurfaceOnly 校验管理端规范的收录范围与隔离性。
func TestAdminSpecCoversAdminSurfaceOnly(t *testing.T) {
	var spec struct {
		BasePath string                     `json:"basePath"`
		Paths    map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(apidocs.AdminSpec, &spec), "管理端规范无法解析，可能需要重新执行 just docs")
	require.NotEmpty(t, spec.Paths)

	assert.Equal(t, "/v1", spec.BasePath)

	// 管理端用户 CRUD 与认证端点必须在管理端文档里。
	for _, path := range []string{
		"/admin/users",
		"/admin/users/{id}",
		"/admin/auth/sign-in/email",
		"/admin/auth/get-session",
		"/admin/auth/token/refresh",
		"/admin/auth/sign-out",
		"/admin/auth/change-password",
	} {
		assert.Contains(t, spec.Paths, path)
	}

	// 管理端文档严格排除前台自助与认证端点。
	for _, path := range []string{"/users/me", "/auth/sign-in/email", "/auth/sign-up/email", "/auth/token/refresh"} {
		assert.NotContains(t, spec.Paths, path, "前台自助端点不应出现在后台管理端文档里")
	}
}
