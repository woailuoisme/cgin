package admin

import (
	"strings"
	"time"

	"github.com/aarondl/opt/omitnull"

	"bgin/internal/database"
	"bgin/internal/database/models"
	"bgin/internal/platform/response"
	"bgin/internal/platform/validator"
	"bgin/internal/shared/enums"
	"bgin/internal/shared/user"
)

func init() {
	validator.RegisterOmitNullTypes(
		omitnull.Val[Role]{},
		omitnull.Val[Status]{},
	)
}

// 分页默认值与上限，对应 api.md 6.1。
const (
	defaultPage     = 1
	defaultPageSize = 20
	maxPageSize     = 100
	// maxPage 是页码硬上限，防止深度翻页引发性能退化。
	maxPage = 1_000_000
)

// Role 是权限角色枚举的本地类型别名，规避 swag 跨包解析缺陷。
type Role = enums.Role

// Status 是账号状态枚举的本地类型别名，规避 swag 跨包解析缺陷。
type Status = enums.Status

// Profile 是共享内核档案视图的类型别名，供本模块的响应体与 Swagger 注解引用。
type Profile = user.Profile

// --- 路径参数 PathParams ---

// UserPathParams 是用户唯一标识的路径参数。
type UserPathParams struct {
	// 用户唯一标识
	ID string `uri:"id" json:"id" example:"usr_01J9X4N8Y0ABCD" validate:"required"`
}

// GetUserPathParams 是 GET /v1/admin/users/:id 的路径参数。
type GetUserPathParams = UserPathParams

// UpdateUserPathParams 是 PATCH /v1/admin/users/:id 的路径参数。
type UpdateUserPathParams = UserPathParams

// DeleteUserPathParams 是 DELETE /v1/admin/users/:id 的路径参数。
type DeleteUserPathParams = UserPathParams

// --- 请求体 Body ---

// CreateUserBody 是 POST /v1/admin/users 的请求体。
type CreateUserBody struct {
	// 用户电子邮箱
	Email string `json:"email" example:"alex@example.com" validate:"required,email,max=254"`
	// 登录口令 (8-128位)
	Password string `json:"password" example:"Secret123!" validate:"required,min=8,max=128"`
	// 用户展示昵称
	Name string `json:"name" example:"Alex Mercer" validate:"required,min=1,max=120"`
	// 唯一登录用户名 (3-30位)
	Username *string `json:"username" example:"alexmercer" validate:"omitempty,min=3,max=30,usernameslug,notreserved"`
	// 用户头像 URL
	Image *string `json:"image" example:"https://example.com/avatar.png" validate:"omitempty,max=2048"`
	// 用户权限角色 (user: 普通用户, admin: 系统管理员)
	Role Role `json:"role" example:"user" enums:"user,admin" validate:"required,oneof=user admin"`
	// 邮箱是否已完成验证
	EmailVerified bool `json:"emailVerified" example:"true"`
}

// UpdateUserBody 是 PATCH /v1/admin/users/:id 的请求体。
type UpdateUserBody struct {
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
	// 邮箱是否已验证
	EmailVerified omitnull.Val[bool] `json:"emailVerified" swaggertype:"boolean" extensions:"x-nullable" example:"true"`
	// 用户权限角色 (user: 普通用户, admin: 系统管理员)
	Role omitnull.Val[Role] `json:"role" validate:"omitempty,oneof=user admin" swaggertype:"string" extensions:"x-nullable" example:"admin" enums:"user,admin"`
	// 账号状态 (active: 正常活跃, inactive: 未激活, banned: 已封禁)
	Status omitnull.Val[Status] `json:"status" validate:"omitempty,oneof=active inactive banned" swaggertype:"string" extensions:"x-nullable" example:"active" enums:"active,inactive,banned"`
	// 是否封禁
	Banned omitnull.Val[bool] `json:"banned" swaggertype:"boolean" extensions:"x-nullable" example:"false"`
	// 封禁原因
	BanReason omitnull.Val[string] `json:"banReason" validate:"omitempty,max=500" swaggertype:"string" extensions:"x-nullable" example:"违规发布广告"`
	// 封禁到期时间 (ISO 8601 UTC)
	BanExpires omitnull.Val[time.Time] `json:"banExpires" swaggertype:"string" extensions:"x-nullable" example:"2026-10-01T00:00:00Z"`
}

// Validate 实现 Validatable 契约，阻断库层非空字段的显式置空。
func (r UpdateUserBody) Validate() error {
	if r.Name.IsNull() {
		return user.FieldCannotBeNull("name")
	}
	if r.Role.IsNull() {
		return user.FieldCannotBeNull("role")
	}
	if r.Status.IsNull() {
		return user.FieldCannotBeNull("status")
	}
	return nil
}

// adminFieldSet 将请求中的档案与管理受控字段投影为共享内核管理字段集。
func (r UpdateUserBody) adminFieldSet() user.AdminFieldSet {
	return user.AdminFieldSet{
		FieldSet: user.FieldSet{
			Name:            r.Name,
			Image:           r.Image,
			Username:        r.Username,
			DisplayUsername: r.DisplayUsername,
			PhoneNumber:     r.PhoneNumber,
		},
		EmailVerified: r.EmailVerified,
		Role:          r.Role,
		Status:        r.Status,
		Banned:        r.Banned,
		BanReason:     r.BanReason,
		BanExpires:    r.BanExpires,
	}
}

// --- 查询参数 QueryParams ---

// ListUsersQueryParams 是 GET /v1/admin/users 的查询参数。
type ListUsersQueryParams struct {
	// 当前页码 (从 1 开始，默认 1，上限 1000000，与 maxPage 常量保持一致)
	Page int `form:"page" json:"page" example:"1" validate:"omitempty,min=1,max=1000000"`
	// 每页条数 (1-100，默认 20)
	PageSize int `form:"pageSize" json:"pageSize" example:"20" validate:"omitempty,min=1,max=100"`
	// 排序字段 (字段名前加 - 表示倒序，如 -createdAt)
	Sort string `form:"sort" json:"sort" example:"-createdAt" validate:"omitempty,usersort,max=120"`
	// 关键字搜索 (按邮箱、昵称、用户名模糊检索)
	Search string `form:"search" json:"search" example:"alex" validate:"omitempty,max=120"`
	// 按账号状态过滤 (active: 正常活跃, inactive: 未激活, banned: 已封禁)
	Status Status `form:"status" json:"status" example:"active" enums:"active,inactive,banned" validate:"omitempty,oneof=active inactive banned"`
	// 按权限角色过滤 (user: 普通用户, admin: 系统管理员)
	Role Role `form:"role" json:"role" example:"user" enums:"user,admin" validate:"omitempty,oneof=user admin"`
}

// ToFilter 将 HTTP 查询参数投影为底层仓储过滤条件，内聚修剪与排序归一化规则。
func (r ListUsersQueryParams) ToFilter() models.UserListFilter {
	return models.UserListFilter{
		Search: strings.TrimSpace(r.Search),
		Status: enums.Status(strings.TrimSpace(string(r.Status))),
		Role:   enums.Role(strings.TrimSpace(string(r.Role))),
		Sort:   user.NormaliseSort(r.Sort),
	}
}

// --- 响应资源 Resource ---

// UserResource 是管理端视角的系统用户视图模型。
type UserResource struct {
	Profile

	// 账号是否已被封禁
	Banned bool `json:"banned" example:"false"`
	// 封禁原因说明
	BanReason *string `json:"banReason" example:"违规发布广告"`
	// 封禁到期时间 (ISO 8601 UTC)
	BanExpires *time.Time `json:"banExpires" example:"2026-10-01T00:00:00Z"`
	// 最后登录时间 (ISO 8601 UTC)
	LastLoginAt *time.Time `json:"lastLoginAt" example:"2026-09-27T10:00:00Z"`
	// 最后登录认证方式
	LastLoginMethod *string `json:"lastLoginMethod" example:"email"`
}

// UserPageResource 是用户分页列表的响应结构。
type UserPageResource struct {
	// 用户列表数据
	Items []UserResource `json:"items"`
	// 分页元信息
	Meta response.Meta `json:"meta"`
}

// toUserResource 把模型投影为管理视角的响应资源。
func toUserResource(entity *models.User) UserResource {
	return UserResource{
		Profile:         user.ProjectProfile(entity),
		Banned:          entity.Banned,
		BanReason:       entity.BanReason,
		BanExpires:      database.UTCPtr(entity.BanExpires),
		LastLoginAt:     database.UTCPtr(entity.LastLoginAt),
		LastLoginMethod: entity.LastLoginMethod,
	}
}

type (
	CreateUserRequest = CreateUserBody
	UpdateUserRequest = UpdateUserBody
	ListUsersQuery    = ListUsersQueryParams
	User              = UserResource
	UserPage          = UserPageResource
)
