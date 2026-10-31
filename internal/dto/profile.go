package dto

import (
	"github.com/aarondl/opt/omitnull"

	"cgin/internal/user"
)

// ProfileResource 为用户内核档案视图的类型别名（供 Swagger 注解直接引用）。
type ProfileResource = user.Profile

// UpdateAvatarBody 接收客户端直传 S3 成功后的头像路径载荷。
type UpdateAvatarBody struct {
	// 头像文件在对象存储中的相对路径（Key）
	AvatarPath string `json:"avatar_path" validate:"required,min=1,max=512" example:"uploads/avatars/20261031/uuid.png"`
}

// UpdateProfileBody 是 PATCH /v1/users/me 的局部更新请求体。
type UpdateProfileBody struct {
	// 用户展示昵称
	Name omitnull.Val[string] `json:"name" validate:"omitempty,min=1,max=120" swaggertype:"string" extensions:"x-nullable" example:"Alex Mercer"`
	// 用户头像在对象存储中的相对路径（Key）
	AvatarPath omitnull.Val[string] `json:"avatar_path" validate:"omitempty,max=512" swaggertype:"string" extensions:"x-nullable" example:"uploads/avatars/20261031/uuid.png"`
	// 唯一登录用户名 (3-30位)
	Username omitnull.Val[string] `json:"username" validate:"omitempty,min=3,max=30,usernameslug,notreserved" swaggertype:"string" extensions:"x-nullable" example:"alexmercer"`
	// 用户显示名称
	DisplayUsername omitnull.Val[string] `json:"displayUsername" validate:"omitempty,min=1,max=120" swaggertype:"string" extensions:"x-nullable" example:"Alex Mercer"`
	// E.164 格式手机号
	PhoneNumber omitnull.Val[string] `json:"phoneNumber" validate:"omitempty,e164" swaggertype:"string" extensions:"x-nullable" example:"+8613800138000"`
}

// Validate 实现 Validatable 契约，阻断库层非空字段的显式置空。
func (b UpdateProfileBody) Validate() error {
	if b.Name.IsNull() {
		return user.FieldCannotBeNull("name")
	}
	return nil
}

// FieldSet 将请求体转换为用户内核的档案字段集。
func (b UpdateProfileBody) FieldSet() user.FieldSet {
	return user.FieldSet{
		Name:            b.Name,
		AvatarPath:      b.AvatarPath,
		Username:        b.Username,
		DisplayUsername: b.DisplayUsername,
		PhoneNumber:     b.PhoneNumber,
	}
}
