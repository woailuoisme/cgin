// Package enum 定义系统跨领域通用枚举与常量。
package enum

import (
	"database/sql"
	"database/sql/driver"
	"encoding"
	"fmt"
	"strings"
)

var (
	_ encoding.TextMarshaler   = OTPType("")
	_ encoding.TextUnmarshaler = (*OTPType)(nil)
	_ driver.Valuer            = OTPType("")
	_ sql.Scanner              = (*OTPType)(nil)
)

// OTPType 代表一次性验证码业务用途枚举。
type OTPType string

// 验证码业务类型常量定义。
const (
	// OTPTypeSignup 注册验证码。
	OTPTypeSignup OTPType = "signup"
	// OTPTypePasswordReset 密码重置验证码。
	OTPTypePasswordReset OTPType = "password_reset"
	// OTPTypeEmailVerification 邮箱验证验证码。
	OTPTypeEmailVerification OTPType = "email_verification"
)

// String 返回验证码用途的字符串表现形式。
func (t OTPType) String() string {
	return string(t)
}

// IsValid 检查验证码用途是否合法。
func (t OTPType) IsValid() bool {
	return t == OTPTypeSignup || t == OTPTypePasswordReset || t == OTPTypeEmailVerification
}

// MarshalText 实现 encoding.TextMarshaler 接口。
func (t OTPType) MarshalText() ([]byte, error) {
	return []byte(t), nil
}

// UnmarshalText 实现 encoding.TextUnmarshaler 接口，在反序列化时严格校验取值白名单。
func (t *OTPType) UnmarshalText(text []byte) error {
	candidate := OTPType(strings.ToLower(strings.TrimSpace(string(text))))
	if !candidate.IsValid() {
		return fmt.Errorf("invalid otp type: %q", string(text))
	}
	*t = candidate
	return nil
}

// Value 实现 driver.Valuer 接口。
func (t OTPType) Value() (driver.Value, error) {
	if !t.IsValid() {
		return nil, fmt.Errorf("invalid otp type value: %q", string(t))
	}
	return string(t), nil
}

// Scan 实现 sql.Scanner 接口。
func (t *OTPType) Scan(value any) error {
	if value == nil {
		*t = ""
		return nil
	}
	var str string
	switch v := value.(type) {
	case string:
		str = v
	case []byte:
		str = string(v)
	default:
		return fmt.Errorf("failed to scan otp type: unexpected type %T", value)
	}
	candidate := OTPType(strings.ToLower(strings.TrimSpace(str)))
	if !candidate.IsValid() {
		return fmt.Errorf("invalid otp type in database: %q", str)
	}
	*t = candidate
	return nil
}

// OTPTypeValues 获取全部验证码用途枚举值。
func OTPTypeValues() []OTPType {
	return []OTPType{
		OTPTypeSignup,
		OTPTypePasswordReset,
		OTPTypeEmailVerification,
	}
}
