// Package enums 定义系统跨领域通用枚举与常量。
package enums

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

// OTPTypeValues 获取全部验证码用途枚举值。
func OTPTypeValues() []OTPType {
	return []OTPType{
		OTPTypeSignup,
		OTPTypePasswordReset,
		OTPTypeEmailVerification,
	}
}
