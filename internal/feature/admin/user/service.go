package adminuser

import (
	"context"
	"io"

	"cgin/internal/database/sqlc"
	"cgin/internal/user"
	"cgin/pkg/media"
	"cgin/pkg/response"
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
	Signer      media.Signer
}

// UserService 实现后台用户管理用例。
type UserService struct {
	userService userStore
	signer      media.Signer
}

// MustNewUserService 构造管理服务；UserService 为装配方必须提供的硬依赖。
func MustNewUserService(deps UserDeps) *UserService {
	if deps.UserService == nil {
		panic("admin: user service requires a non-nil UserService")
	}
	return &UserService{userService: deps.UserService, signer: deps.Signer}
}

// Signer 返回媒体签名器。
func (s *UserService) Signer() media.Signer {
	return s.signer
}

// ListUsers 返回分页用户列表与分页元信息。
func (s *UserService) ListUsers(ctx context.Context, filter user.ListFilter, page, pageSize int) ([]sqlc.User, response.Meta, error) {
	users, total, err := s.userService.List(ctx, filter, page, pageSize)
	if err != nil {
		return nil, response.Meta{}, err
	}
	return users, response.NewMeta(page, pageSize, total), nil
}

// ExportUsers 流式导出匹配条件的所有用户数据至目标 io.Writer，采用键集游标分块拉取杜绝 OOM。
func (s *UserService) ExportUsers(ctx context.Context, filter user.ListFilter, w io.Writer) error {
	return ExportUsers(ctx, w, s.userService.ListAfter, filter)
}

// GetUser 返回指定用户。
func (s *UserService) GetUser(ctx context.Context, id string) (*sqlc.User, error) {
	return s.userService.Get(ctx, id)
}

// CreateUser 由管理员直接创建账号。
func (s *UserService) CreateUser(ctx context.Context, params user.RegisterParams) (*sqlc.User, error) {
	return s.userService.Register(ctx, params)
}

// UpdateUser 由管理员局部更新用户。
func (s *UserService) UpdateUser(ctx context.Context, id string, fields user.AdminFieldSet) (*sqlc.User, error) {
	return s.userService.UpdateAdminUser(ctx, id, fields)
}

// DeleteUser 删除指定用户。
func (s *UserService) DeleteUser(ctx context.Context, id string) error {
	return s.userService.Delete(ctx, id)
}
