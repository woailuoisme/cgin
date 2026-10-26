// Package enums 定义系统跨领域通用枚举与常量。
package enums

import (
	"fmt"
	"slices"
	"strings"
)

// AdminRole 代表后台管理系统的授权角色枚举。
type AdminRole string

// 管理员角色常量定义。
const (
	// SuperAdmin 超级管理员。
	SuperAdmin AdminRole = "super_admin"
	// Admin 普通管理员。
	Admin AdminRole = "admin"
	// Editor 运营编辑。
	Editor AdminRole = "editor"
	// Agent 代理人员。
	Agent AdminRole = "agent"
	// Operator 操作员。
	Operator AdminRole = "operator"
	// Ops 运维人员。
	Ops AdminRole = "ops"
	// RepWorker 客服人员。
	RepWorker AdminRole = "rep_worker"
)

// SuperAdminAlias 表示兼容历史拼写（无下划线）的超级管理员角色输入别名。
const SuperAdminAlias AdminRole = "superadmin"

// IsAdminGuardRole 判断给定角色值是否具备进入管理端认证守卫的资格。
func IsAdminGuardRole(role string) bool {
	switch AdminRole(strings.ToLower(strings.TrimSpace(role))) {
	case SuperAdmin, SuperAdminAlias, Admin:
		return true
	default:
		return false
	}
}

// Role 定义用户系统的权限角色枚举。
type Role string

// 普通用户与管理员角色常量定义。
const (
	// RoleUser 普通注册用户。
	RoleUser Role = "user"
	// RoleAdmin 系统管理员。
	RoleAdmin Role = "admin"
)

// UserRole 代表普通用户授权角色枚举别名，保持向前兼容。
type UserRole = Role

// String 返回角色的字符串表现形式。
func (r Role) String() string {
	return string(r)
}

// IsValid 校验角色取值是否在合法白名单内。
func (r Role) IsValid() bool {
	return r == RoleUser || r == RoleAdmin
}

// IsAdmin 判定当前角色是否为系统管理员。
func (r Role) IsAdmin() bool {
	return r == RoleAdmin
}

// AllRoles 返回全部候选角色切片。
func AllRoles() []Role {
	return []Role{RoleUser, RoleAdmin}
}

// allAdminRoles 是管理员角色全集，仅通过 AdminRoleValues 以拷贝形式对外暴露，
// 避免调用方拿到切片底层数组后篡改全局枚举。
var allAdminRoles = []AdminRole{
	SuperAdmin,
	Admin,
	Editor,
	Agent,
	Operator,
	Ops,
	RepWorker,
}

// String 返回管理员角色的字符串表现形式。
func (r AdminRole) String() string {
	return string(r)
}

// IsValid 检查角色是否在合法预设角色集合中。
func (r AdminRole) IsValid() bool {
	switch r {
	case SuperAdmin, Admin, Editor, Agent, Operator, Ops, RepWorker:
		return true
	default:
		return false
	}
}

// AdminRoleFromString 解析字符串为管理员角色枚举。
func AdminRoleFromString(value string) (AdminRole, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == string(SuperAdminAlias) || normalized == "super admin" {
		return SuperAdmin, nil
	}

	role := AdminRole(normalized)
	if role.IsValid() {
		return role, nil
	}

	return "", fmt.Errorf("invalid admin role: %s", value)
}

// AdminRoleMustFromString 解析字符串为角色枚举，失败时触发恐慌。
func AdminRoleMustFromString(value string) AdminRole {
	role, err := AdminRoleFromString(value)
	if err != nil {
		panic(err)
	}

	return role
}

// AdminRoleValues 返回所有管理员角色切片（每次返回独立拷贝）。
func AdminRoleValues() []AdminRole {
	return slices.Clone(allAdminRoles)
}
