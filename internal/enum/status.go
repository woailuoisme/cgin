// Package enum 定义系统跨领域通用枚举与常量。
package enum

import (
	"database/sql"
	"database/sql/driver"
	"encoding"
	"fmt"
	"slices"
	"strings"
)

var (
	_ encoding.TextMarshaler   = UserStatus("")
	_ encoding.TextUnmarshaler = (*UserStatus)(nil)
	_ driver.Valuer            = UserStatus("")
	_ sql.Scanner              = (*UserStatus)(nil)
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

// MarshalText 实现 encoding.TextMarshaler 接口。
func (s UserStatus) MarshalText() ([]byte, error) {
	return []byte(s), nil
}

// UnmarshalText 实现 encoding.TextUnmarshaler 接口，在反序列化时严格校验取值白名单。
func (s *UserStatus) UnmarshalText(text []byte) error {
	status, err := UserStatusFromString(string(text))
	if err != nil {
		return err
	}
	*s = status
	return nil
}

// Value 实现 driver.Valuer 接口。
func (s UserStatus) Value() (driver.Value, error) {
	if !s.IsValid() {
		return nil, fmt.Errorf("invalid user status value: %q", string(s))
	}
	return string(s), nil
}

// Scan 实现 sql.Scanner 接口。
func (s *UserStatus) Scan(value any) error {
	if value == nil {
		*s = ""
		return nil
	}
	var str string
	switch v := value.(type) {
	case string:
		str = v
	case []byte:
		str = string(v)
	default:
		return fmt.Errorf("failed to scan user status: unexpected type %T", value)
	}
	status, err := UserStatusFromString(str)
	if err != nil {
		return fmt.Errorf("invalid user status in database: %w", err)
	}
	*s = status
	return nil
}

// UserStatusValues 获取全部用户状态枚举值列表（每次返回独立拷贝）。
func UserStatusValues() []UserStatus {
	return slices.Clone(allUserStatuses)
}
