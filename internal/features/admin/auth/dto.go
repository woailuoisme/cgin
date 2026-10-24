package adminauth

import (
	"bgin/internal/shared/user"
)

// Profile 是共享内核档案视图的类型别名，避免 swag 跨包解析失效。
type Profile = user.Profile

// SignInEmailBody 是 POST /v1/admin/auth/sign-in/email 的请求体。
type SignInEmailBody struct {
	// 管理员电子邮箱
	Email string `json:"email" example:"admin@example.com" validate:"required,email"`
	// 管理员登录口令
	Password string `json:"password" example:"Admin123!" validate:"required"`
}

// RefreshTokenBody 是 POST /v1/admin/auth/token/refresh 的请求体。
type RefreshTokenBody struct {
	// 刷新令牌凭据
	RefreshToken string `json:"refreshToken" example:"rt_01J9X4N8Y0EFGH" validate:"required"`
}

// SignOutBody 是 POST /v1/admin/auth/sign-out 的请求体。
type SignOutBody struct {
	// 待作废的刷新令牌 (可选，同时作废可真正阻断续签)
	RefreshToken *string `json:"refreshToken" example:"rt_01J9X4N8Y0EFGH" validate:"omitempty"`
}

// ChangePasswordBody 是 POST /v1/admin/auth/change-password 的请求体。
type ChangePasswordBody struct {
	// 当前登录口令
	CurrentPassword string `json:"currentPassword" example:"Admin123!" validate:"required"`
	// 新登录口令 (8-128位，须含字母与数字)
	NewPassword string `json:"newPassword" example:"NewAdmin456!" validate:"required,min=8,max=128"`
}

// SessionResource 是签发令牌类端点的统一响应体。
type SessionResource struct {
	// 访问令牌 (JWT Access Token)
	Token string `json:"token" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`
	// 刷新令牌 (Refresh Token)
	RefreshToken string `json:"refreshToken" example:"rt_01J9X4N8Y0EFGH"`
	// 访问令牌有效期（秒）
	ExpiresIn int `json:"expiresIn" example:"900"`
	// 管理员身份资料投影
	User Profile `json:"user"`
}

// GetSessionResource 是 GET /v1/admin/auth/get-session 的响应体。
type GetSessionResource struct {
	// 当前登录管理员身份资料投影
	User Profile `json:"user"`
}

type (
	SignInEmailRequest    = SignInEmailBody
	RefreshTokenRequest   = RefreshTokenBody
	SignOutRequest        = SignOutBody
	ChangePasswordRequest = ChangePasswordBody
	SessionResponse       = SessionResource
	GetSessionResponse    = GetSessionResource
)
