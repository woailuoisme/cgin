// Package enums 定义系统跨领域通用枚举与常量。
package enums

import (
	"fmt"
	"slices"
	"strings"
)

// UserStatus 代表用户账号状态枚举。
type UserStatus string

// 用户状态枚举常量定义。
const (
	// UserStatusActive 正常活跃状态。
	UserStatusActive UserStatus = "active"
	// UserStatusInactive 未激活状态。
	UserStatusInactive UserStatus = "inactive"
	// UserStatusPending 等待审核状态。
	UserStatusPending UserStatus = "pending"
	// UserStatusSuspended 暂停使用状态。
	UserStatusSuspended UserStatus = "suspended"
	// UserStatusBanned 封禁冻结状态。
	UserStatusBanned UserStatus = "banned"
)

// Status 定义用户账号的生命周期状态枚举别名。
type Status = UserStatus

// 账号状态通用常量定义。
const (
	// StatusActive 正常活跃状态。
	StatusActive Status = UserStatusActive
	// StatusInactive 未激活状态。
	StatusInactive Status = UserStatusInactive
	// StatusPending 等待审核状态。
	StatusPending Status = UserStatusPending
	// StatusSuspended 暂停使用状态。
	StatusSuspended Status = UserStatusSuspended
	// StatusBanned 封禁冻结状态。
	StatusBanned Status = UserStatusBanned
)

// allUserStatuses 是用户状态全集，仅通过 UserStatusValues 以拷贝形式对外暴露，
// 避免调用方拿到切片底层数组后篡改全局枚举。
var allUserStatuses = []UserStatus{
	UserStatusActive,
	UserStatusInactive,
	UserStatusPending,
	UserStatusSuspended,
	UserStatusBanned,
}

// AllStatuses 返回常用候选账号状态切片。
func AllStatuses() []Status {
	return []Status{StatusActive, StatusInactive, StatusBanned}
}

// String 返回状态的字符串表现形式。
func (s UserStatus) String() string {
	return string(s)
}

// IsValid 检查状态是否在预设合法状态集中。
func (s UserStatus) IsValid() bool {
	switch s {
	case UserStatusActive, UserStatusInactive, UserStatusPending, UserStatusSuspended, UserStatusBanned:
		return true
	default:
		return false
	}
}

// IsActive 判断用户是否处于正常活跃状态。
func (s UserStatus) IsActive() bool {
	return s == UserStatusActive
}

// IsBlocked 判断用户是否已被冻结或封禁。
func (s UserStatus) IsBlocked() bool {
	return s == UserStatusSuspended || s == UserStatusBanned
}

// CanLogin 判断用户是否允许登录认证。
func (s UserStatus) CanLogin() bool {
	return s == UserStatusActive || s == UserStatusPending
}

// IsBanned 判定账号是否已被封禁冻结。
func (s UserStatus) IsBanned() bool {
	return s == UserStatusBanned
}

// UserStatusFromString 解析字符串为用户状态枚举。
func UserStatusFromString(value string) (UserStatus, error) {
	status := UserStatus(strings.ToLower(strings.TrimSpace(value)))
	if status.IsValid() {
		return status, nil
	}

	return "", fmt.Errorf("invalid user status: %s", value)
}

// UserStatusMustFromString 解析字符串为用户状态枚举，失败时触发恐慌。
func UserStatusMustFromString(value string) UserStatus {
	status, err := UserStatusFromString(value)
	if err != nil {
		panic(err)
	}

	return status
}

// UserStatusValues 获取全部用户状态枚举值列表（每次返回独立拷贝）。
func UserStatusValues() []UserStatus {
	return slices.Clone(allUserStatuses)
}
