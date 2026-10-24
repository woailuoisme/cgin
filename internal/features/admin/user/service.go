package admin

import (
	"context"
	"io"
	"strings"

	"github.com/samber/lo"
	"github.com/uptrace/bun"

	"bgin/internal/database/models"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/excel"
	"bgin/internal/platform/response"
	"bgin/internal/platform/validator"
	"bgin/internal/shared/enums"
	"bgin/internal/shared/user"
)

// passwordHasher 声明用户管理服务所需的口令安全哈希能力（遵循 Uber Go Guide：接口由消费方定义）。
type passwordHasher interface {
	Make(password string) (string, error)
}

// Deps 声明用户管理服务的外部依赖。
type Deps struct {
	DB          *bun.DB
	UserService *user.Service
	Hasher      passwordHasher
}

// Service 实现后台用户管理用例。
type Service struct {
	db          bun.IDB
	userService *user.Service
	hasher      passwordHasher
}

// NewService 构造管理服务。
func NewService(deps Deps) *Service {
	userService := deps.UserService
	if userService == nil && deps.DB != nil {
		userService = user.NewService(deps.DB)
	}
	return &Service{
		db:          deps.DB,
		userService: userService,
		hasher:      deps.Hasher,
	}
}

// ListUsers 返回分页用户列表，排序与过滤都走白名单。
func (s *Service) ListUsers(ctx context.Context, query ListUsersQueryParams) ([]UserResource, response.Meta, error) {
	page, pageSize := NormalisePagination(query.Page, query.PageSize)
	users, total, err := s.userService.List(ctx, query.ToFilter(), page, pageSize)
	if err != nil {
		return nil, response.Meta{}, err
	}

	items := lo.Map(users, func(entity models.User, _ int) UserResource {
		return toUserResource(&entity)
	})
	return items, response.NewMeta(page, pageSize, total), nil
}

// ExportUsers 流式导出匹配条件的所有用户数据至目标 io.Writer，采用物理分块拉取杜绝 OOM。
func (s *Service) ExportUsers(ctx context.Context, query ListUsersQueryParams, w io.Writer, opts ...excel.ExportOption) error {
	return ExportChunkedStream(ctx, w, UserFetchFunc(s.userService.ListChunk), query.ToFilter(), opts...)
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
	if err := validator.PasswordStrength("password", req.Password); err != nil {
		return UserResource{}, err
	}

	entity := &models.User{
		Name:          strings.TrimSpace(req.Name),
		Email:         user.NormaliseEmail(req.Email),
		EmailVerified: req.EmailVerified,
		Image:         req.Image,
		Role:          req.Role,
		Status:        enums.StatusActive,
	}
	if req.Username != nil {
		normalised := user.NormaliseUsername(*req.Username)
		entity.Username = &normalised
		entity.DisplayUsername = &normalised
	}

	hash, err := s.hasher.Make(req.Password)
	if err != nil {
		return UserResource{}, errs.Internal(err)
	}

	created, err := s.userService.CreateWithCredential(ctx, entity, hash)
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
