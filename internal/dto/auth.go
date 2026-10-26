// Package dto 汇集客户端 HTTP 传输契约：请求/响应模型与模型投影。
package dto

import (
	"errors"
	"strings"

	"cgin/pkg/validator"
)

// SignUpEmailBody 是 POST /v1/auth/sign-up/email 的请求体。
type SignUpEmailBody struct {
	// 用户电子邮箱
	Email string `json:"email" example:"alex@example.com" validate:"required,email,max=254"`
	// 登录口令 (8-128位)
	Password string `json:"password" example:"Secret123!" validate:"required,min=8,max=128"`
	// 用户展示昵称
	Name string `json:"name" example:"Alex Mercer" validate:"required,min=1,max=120"`
	// 登录用户名 (3-30位小写字母数字及下划线，不可使用系统保留字)
	Username *string `json:"username" example:"alexmercer" validate:"omitempty,min=3,max=30,usernameslug,notreserved"`
	// 用户头像 URL
	Image *string `json:"image" example:"https://example.com/avatar.png" validate:"omitempty,max=2048"`
}

// SignInEmailBody 是 POST /v1/auth/sign-in/email 的请求体。
type SignInEmailBody struct {
	// 用户电子邮箱
	Email string `json:"email" example:"alex@example.com" validate:"required,email"`
	// 登录口令
	Password string `json:"password" example:"Secret123!" validate:"required"`
}

// SignInUsernameBody 是 POST /v1/auth/sign-in/username 的请求体。
type SignInUsernameBody struct {
	// 登录用户名 (3-30位)
	Username string `json:"username" example:"alexmercer" validate:"required,min=3,max=30"`
	// 登录口令
	Password string `json:"password" example:"Secret123!" validate:"required"`
}

// SignOutBody 是 POST /v1/auth/sign-out 的请求体。
type SignOutBody struct {
	// 待作废的刷新令牌 (可选，同时作废可真正阻断续签)
	RefreshToken *string `json:"refreshToken" example:"rt_01J9X4N8Y0EFGH" validate:"omitempty"`
}

// RefreshTokenBody 是 POST /v1/auth/token/refresh 的请求体。
type RefreshTokenBody struct {
	// 刷新令牌凭据
	RefreshToken string `json:"refreshToken" example:"rt_01J9X4N8Y0EFGH" validate:"required"`
}

// ChangePasswordBody 是 POST /v1/auth/change-password 的请求体。
type ChangePasswordBody struct {
	// 当前登录口令
	CurrentPassword string `json:"currentPassword" example:"Secret123!" validate:"required"`
	// 新登录口令 (8-128位)
	NewPassword string `json:"newPassword" example:"NewSecret456!" validate:"required,min=8,max=128"`
}

// ForgetPasswordBody 是 POST /v1/auth/forget-password 的请求体，触发重置链接。
type ForgetPasswordBody struct {
	// 用户注册电子邮箱
	Email string `json:"email" example:"alex@example.com" validate:"required,email"`
}

// RequestPasswordResetBody 是 POST /v1/auth/request-password-reset 的请求体，触发重置验证码。
type RequestPasswordResetBody struct {
	// 用户注册电子邮箱
	Email string `json:"email" example:"alex@example.com" validate:"required,email"`
}

// ResetPasswordBody 是 POST /v1/auth/reset-password 的请求体。
type ResetPasswordBody struct {
	// 绑定的电子邮箱
	Email string `json:"email" example:"alex@example.com" validate:"required,email"`
	// 密码重置令牌 (链接路径凭据，与验证码二选一)
	Token *string `json:"token" example:"tok_01J9X4N8Y0RESET" validate:"omitempty,min=16"`
	// 6位数字重置验证码 (验证码路径凭据，与令牌二选一)
	OTP *string `json:"otp" example:"123456" validate:"omitempty,len=6,numeric"`
	// 新登录口令 (8-128位)
	NewPassword string `json:"newPassword" example:"NewSecret456!" validate:"required,min=8,max=128"`
}

var _ validator.Validatable = ResetPasswordBody{}

// Validate 实现 Validatable 契约，校验 Token 或 OTP 至少提供其一。
func (b ResetPasswordBody) Validate() error {
	hasToken := b.Token != nil && strings.TrimSpace(*b.Token) != ""
	hasOTP := b.OTP != nil && strings.TrimSpace(*b.OTP) != ""
	if !hasToken && !hasOTP {
		return errors.New("either token or otp must be provided")
	}
	return nil
}

// SendVerificationOTPBody 是 POST /v1/auth/send-verification-otp 的请求体。
type SendVerificationOTPBody struct {
	// 待验证的用户电子邮箱
	Email string `json:"email" example:"alex@example.com" validate:"required,email"`
}

// VerifyEmailBody 是 POST /v1/auth/verify-email 的请求体。
type VerifyEmailBody struct {
	// 待验证的用户电子邮箱
	Email string `json:"email" example:"alex@example.com" validate:"required,email"`
	// 6位数字邮箱验证码
	OTP string `json:"otp" example:"123456" validate:"required,len=6,numeric"`
}

// SessionResource 是签发令牌类端点的统一响应体。
type SessionResource struct {
	// 访问令牌 (JWT Access Token)
	Token string `json:"token" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`
	// 刷新令牌 (Refresh Token)
	RefreshToken string `json:"refreshToken,omitempty" example:"rt_01J9X4N8Y0EFGH"`
	// 访问令牌有效期（秒）
	ExpiresIn int `json:"expiresIn" example:"900"`
	// 用户身份资料投影
	User UserResource `json:"user"`
}

// GetSessionResource 是 GET /v1/auth/get-session 的响应体。
type GetSessionResource struct {
	// 当前登录用户身份资料投影
	User UserResource `json:"user"`
}

// StatusResource 是通用布尔状态响应体。
type StatusResource struct {
	// 请求是否已成功受理并处理完毕
	Status bool `json:"status" example:"true"`
}
