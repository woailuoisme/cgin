package profile

import (
	"mime/multipart"

	"github.com/aarondl/opt/omitnull"

	"bgin/internal/shared/user"
)

// ProfileResource 为共享内核用户档案视图的类型别名（供 Swagger 注解直接引用）。
type ProfileResource = user.Profile

// UploadAvatarBody 接收 multipart/form-data 格式的用户头像上传表单载荷。
type UploadAvatarBody struct {
	// 头像图片文件（最大 2MB，支持 JPEG/PNG/WebP）
	Avatar *multipart.FileHeader `form:"avatar" json:"avatar" validate:"required,file,image,mimes=jpg jpeg png webp,maxsize=2M"`
}

// UpdateProfileBody 是 PATCH /v1/users/me 的局部更新请求体。
type UpdateProfileBody struct {
	// 用户展示昵称
	Name omitnull.Val[string] `json:"name" validate:"omitempty,min=1,max=120" swaggertype:"string" extensions:"x-nullable" example:"Alex Mercer"`
	// 用户头像 URL
	Image omitnull.Val[string] `json:"image" validate:"omitempty,max=2048" swaggertype:"string" extensions:"x-nullable" example:"https://example.com/avatar.png"`
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

// fieldSet 将请求转换为共享内核档案字段集。
func (b UpdateProfileBody) fieldSet() user.FieldSet { return user.FieldSet(b) }

type (
	Profile              = ProfileResource
	UpdateProfileRequest = UpdateProfileBody
)
