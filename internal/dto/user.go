package dto

import (
	"time"

	"cgin/internal/database/sqlc"
	"cgin/internal/enum"
	appuser "cgin/internal/user"
)

// UserResource 是认证端点与管理端用户接口共用的用户档案资源视图。
type UserResource struct {
	// 用户唯一标识
	ID string `json:"id" example:"usr_01J9X4N8Y0ABCD"`
	// 用户展示昵称
	Name string `json:"name" example:"Alex Mercer"`
	// 用户电子邮箱
	Email string `json:"email" example:"alex@example.com"`
	// 邮箱是否已完成验证
	EmailVerified bool `json:"emailVerified" example:"true"`
	// 用户头像 URL
	Image *string `json:"image" example:"https://example.com/avatar.png"`
	// 用户权限角色 (user: 普通用户, admin: 系统管理员)
	Role enum.Role `json:"role" example:"user" enums:"user,admin"`
	// 唯一登录用户名
	Username *string `json:"username" example:"alexmercer"`
	// 用户显示名称
	DisplayUsername *string `json:"displayUsername" example:"Alex Mercer"`
	// E.164 格式手机号
	PhoneNumber *string `json:"phoneNumber" example:"+8613800138000"`
	// 账号创建时间 (ISO 8601 UTC)
	CreatedAt time.Time `json:"createdAt" example:"2026-09-27T10:00:00Z"`
	// 账号更新时间 (ISO 8601 UTC)
	UpdatedAt time.Time `json:"updatedAt" example:"2026-09-27T10:00:00Z"`
}

// ProjectUser 把用户模型投影为用户档案资源视图。
//
// 逐字段赋值而不是依赖结构体布局转换：两端的字段增减不会因“恰好同构”而静默通过。
func ProjectUser(u *sqlc.User) UserResource {
	profile := appuser.ProjectProfile(u)
	return UserResource{
		ID:              profile.ID,
		Name:            profile.Name,
		Email:           profile.Email,
		EmailVerified:   profile.EmailVerified,
		Image:           profile.Image,
		Role:            profile.Role,
		Username:        profile.Username,
		DisplayUsername: profile.DisplayUsername,
		PhoneNumber:     profile.PhoneNumber,
		CreatedAt:       profile.CreatedAt,
		UpdatedAt:       profile.UpdatedAt,
	}
}
