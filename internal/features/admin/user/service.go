package admin

import (
	"context"
	"io"

	"github.com/samber/lo"

	"cgin/internal/database/sqlc"
	"cgin/internal/platform/response"
	"cgin/internal/shared/enums"
	"cgin/internal/shared/user"
)

// Deps 声明用户管理服务的外部依赖。
type Deps struct {
	UserService *user.Service
}

// Service 实现后台用户管理用例。
type Service struct {
	userService *user.Service
}

// NewService 构造管理服务；UserService 为装配方必须提供的硬依赖。
func NewService(deps Deps) *Service {
	if deps.UserService == nil {
		panic("admin: user service requires a non-nil UserService")
	}
	return &Service{userService: deps.UserService}
}

// ListUsers 返回分页用户列表，排序与过滤都走白名单。
func (s *Service) ListUsers(ctx context.Context, query ListUsersQueryParams) ([]UserResource, response.Meta, error) {
	page, pageSize := NormalisePagination(query.Page, query.PageSize)
	users, total, err := s.userService.List(ctx, query.ToFilter(), page, pageSize)
	if err != nil {
		return nil, response.Meta{}, err
	}

	items := lo.Map(users, func(entity sqlc.User, _ int) UserResource {
		return toUserResource(&entity)
	})
	return items, response.NewMeta(page, pageSize, total), nil
}

// ExportUsers 流式导出匹配条件的所有用户数据至目标 io.Writer，采用物理分块拉取杜绝 OOM。
func (s *Service) ExportUsers(ctx context.Context, query ListUsersQueryParams, w io.Writer) error {
	return writeUsersXLSX(ctx, w, s.userService.ListChunk, query.ToFilter())
}

// GetUser 返回管理视角的单个用户。
func (s *Service) GetUser(ctx context.Context, id string) (UserResource, error) {
	entity, err := s.userService.Get(ctx, id)
	if err != nil {
		return UserResource{}, err
	}
	return toUserResource(entity), nil
}

// CreateUser 由管理员直接创建账号，初始口令由调用方指定。
func (s *Service) CreateUser(ctx context.Context, req CreateUserBody) (UserResource, error) {
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
		return UserResource{}, err
	}
	return toUserResource(created), nil
}

// UpdateUser 由管理员局部更新用户，含角色、状态与封禁信息。
func (s *Service) UpdateUser(ctx context.Context, id string, req UpdateUserBody) (UserResource, error) {
	entity, err := s.userService.UpdateAdminUser(ctx, id, req.adminFieldSet())
	if err != nil {
		return UserResource{}, err
	}
	return toUserResource(entity), nil
}

// DeleteUser 软删除指定用户。
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	return s.userService.Delete(ctx, id)
}

// NormalisePagination 将分页参数收敛到允许的安全区间。
func NormalisePagination(page, pageSize int) (int, int) {
	page = min(max(page, defaultPage), maxPage)
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	return page, min(pageSize, maxPageSize)
}
