package adminservice

import (
	"context"
	"io"

	"github.com/samber/lo"

	"cgin/internal/database/sqlc"
	admindto "cgin/internal/dto/admin"
	"cgin/pkg/response"
	"cgin/shared/enums"
	"cgin/shared/user"
)

// userStore 声明后台用户管理所需的用户读写能力（接口由消费方定义）。
type userStore interface {
	List(ctx context.Context, filter user.ListFilter, page, pageSize int) ([]sqlc.User, int, error)
	ListAfter(ctx context.Context, filter user.ListFilter, afterID string, limit int) ([]sqlc.User, error)
	Get(ctx context.Context, id string) (*sqlc.User, error)
	Register(ctx context.Context, params user.RegisterParams) (*sqlc.User, error)
	UpdateAdminUser(ctx context.Context, id string, fields user.AdminFieldSet) (*sqlc.User, error)
	Delete(ctx context.Context, id string) error
}

// UserDeps 声明用户管理服务的外部依赖。
type UserDeps struct {
	UserService userStore
}

// UserService 实现后台用户管理用例。
type UserService struct {
	userService userStore
}

// MustNewUserService 构造管理服务；UserService 为装配方必须提供的硬依赖。
func MustNewUserService(deps UserDeps) *UserService {
	if deps.UserService == nil {
		panic("admin: user service requires a non-nil UserService")
	}
	return &UserService{userService: deps.UserService}
}

// ListUsers 返回分页用户列表，排序与过滤都走白名单。
func (s *UserService) ListUsers(ctx context.Context, query admindto.ListUsersQueryParams) ([]admindto.UserResource, response.Meta, error) {
	page, pageSize := admindto.NormalisePagination(query.Page, query.PageSize)
	users, total, err := s.userService.List(ctx, query.ToFilter(), page, pageSize)
	if err != nil {
		return nil, response.Meta{}, err
	}

	items := lo.Map(users, func(entity sqlc.User, _ int) admindto.UserResource {
		return admindto.ToUserResource(&entity)
	})
	return items, response.NewMeta(page, pageSize, total), nil
}

// ExportUsers 流式导出匹配条件的所有用户数据至目标 io.Writer，采用键集游标分块拉取杜绝 OOM。
func (s *UserService) ExportUsers(ctx context.Context, query admindto.ListUsersQueryParams, w io.Writer) error {
	return writeUsersXLSX(ctx, w, s.userService.ListAfter, query.ToFilter())
}

// GetUser 返回管理视角的单个用户。
func (s *UserService) GetUser(ctx context.Context, id string) (admindto.UserResource, error) {
	entity, err := s.userService.Get(ctx, id)
	if err != nil {
		return admindto.UserResource{}, err
	}
	return admindto.ToUserResource(entity), nil
}

// CreateUser 由管理员直接创建账号，初始口令由调用方指定。
func (s *UserService) CreateUser(ctx context.Context, req admindto.CreateUserBody) (admindto.UserResource, error) {
	created, err := s.userService.Register(ctx, user.RegisterParams{
		Name:          req.Name,
		Email:         req.Email,
		Username:      req.Username,
		Image:         req.Image,
		Role:          req.Role,
		Status:        enums.StatusActive,
		EmailVerified: req.EmailVerified,
		Password:      req.Password,
	})
	if err != nil {
		return admindto.UserResource{}, err
	}
	return admindto.ToUserResource(created), nil
}

// UpdateUser 由管理员局部更新用户，含角色、状态与封禁信息。
func (s *UserService) UpdateUser(ctx context.Context, id string, req admindto.UpdateUserBody) (admindto.UserResource, error) {
	entity, err := s.userService.UpdateAdminUser(ctx, id, req.AdminFieldSet())
	if err != nil {
		return admindto.UserResource{}, err
	}
	return admindto.ToUserResource(entity), nil
}

// DeleteUser 软删除指定用户。
func (s *UserService) DeleteUser(ctx context.Context, id string) error {
	return s.userService.Delete(ctx, id)
}
