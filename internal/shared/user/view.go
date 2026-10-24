package user

import (
	"time"

	"cgin/internal/database/sqlc"
	"cgin/internal/shared/enums"
)

// PublicProfile 是用户公共档案视图，客户端与管理端共用的唯一字段来源。
type PublicProfile struct {
	// 用户唯一标识
	ID string `json:"id" example:"usr_01J9X4N8Y0ABCD"`
	// 用户展示昵称
	Name string `json:"name" example:"Alex Mercer"`
	// 用户电子邮箱
	Email string `json:"email" example:"alex@example.com"`
	// 邮箱是否已验证
	EmailVerified bool `json:"emailVerified" example:"true"`
	// 用户头像 URL
	Image *string `json:"image" example:"https://example.com/avatar.png"`
	// 用户权限角色 (user: 普通用户, admin: 系统管理员)
	Role enums.Role `json:"role" example:"user" enums:"user,admin"`
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

// Profile 是用户档案响应视图，在公共字段之上扩展账号状态。
type Profile struct {
	PublicProfile
	// 账号状态 (active: 正常活跃, inactive: 未激活, banned: 已封禁)
	Status enums.Status `json:"status" example:"active" enums:"active,inactive,banned"`
}

// ProjectPublicProfile 把用户行投影为公共档案视图。
func ProjectPublicProfile(entity *sqlc.User) PublicProfile {
	if entity == nil {
		return PublicProfile{}
	}
	return PublicProfile{
		ID:              entity.ID,
		Name:            entity.Name,
		Email:           entity.Email,
		EmailVerified:   entity.EmailVerified,
		Image:           entity.Image,
		Role:            entity.Role,
		Username:        entity.Username,
		DisplayUsername: entity.DisplayUsername,
		PhoneNumber:     entity.PhoneNumber,
		// 归一到 UTC：避免响应体带出服务器本地偏移。
		CreatedAt: entity.CreatedAt.UTC(),
		UpdatedAt: entity.UpdatedAt.UTC(),
	}
}

// ProjectProfile 把用户行投影为完整档案视图。
func ProjectProfile(entity *sqlc.User) Profile {
	if entity == nil {
		return Profile{}
	}
	return Profile{
		PublicProfile: ProjectPublicProfile(entity),
		Status:        entity.Status,
	}
}
