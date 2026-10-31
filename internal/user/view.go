package user

import (
	"time"

	"cgin/internal/database/sqlc"
	"cgin/internal/enum"
)

// Profile 是用户档案视图：客户端与管理端共用的唯一字段来源。
//
// 字段逐项声明而不是嵌入其他结构体：嵌入会让 JSON 与 OpenAPI 契约
// 随内层类型的演化被动漂移，读取字段集也要跨文件跳转。
type Profile struct {
	// 用户唯一标识
	ID string `json:"id" example:"usr_01J9X4N8Y0ABCD"`
	// 用户展示昵称
	Name string `json:"name" example:"Alex Mercer"`
	// 用户电子邮箱
	Email string `json:"email" example:"alex@example.com"`
	// 邮箱是否已验证
	EmailVerified bool `json:"emailVerified" example:"true"`
	// 用户头像在对象存储中的相对路径
	AvatarPath *string `json:"avatarPath" example:"avatars/20261031/uuid.webp"`
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
	// 账号状态 (active: 正常活跃, inactive: 未激活, banned: 已封禁)
	Status enum.Status `json:"status" example:"active" enums:"active,inactive,banned"`
}

// ProjectProfile 把用户行投影为档案视图；时间统一归一到 UTC，避免响应体带出服务器本地偏移。
func ProjectProfile(entity *sqlc.User) Profile {
	if entity == nil {
		return Profile{}
	}
	return Profile{
		ID:              entity.ID,
		Name:            entity.Name,
		Email:           entity.Email,
		EmailVerified:   entity.EmailVerified,
		AvatarPath:      entity.AvatarPath,
		Role:            entity.Role,
		Username:        entity.Username,
		DisplayUsername: entity.DisplayUsername,
		PhoneNumber:     entity.PhoneNumber,
		CreatedAt:       entity.CreatedAt.UTC(),
		UpdatedAt:       entity.UpdatedAt.UTC(),
		Status:          entity.Status,
	}
}
