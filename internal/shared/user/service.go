package user

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/samber/lo"

	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/internal/platform/errs"
	"cgin/internal/platform/hasher"
	"cgin/internal/shared/enums"
)

// Scope 声明档案更新的权限作用域。
type Scope string

const (
	// ScopeClient 声明客户端用户本人档案更新作用域，仅允许修改受限的展示档案字段。
	ScopeClient Scope = "client"
	// ScopeAdmin 声明管理端管理员更新作用域，具备完全字段可写权限。
	ScopeAdmin Scope = "admin"
)

// Service 提供高内聚、高杠杆的用户档案领域服务。
type Service struct {
	db     *database.DB
	hasher *hasher.Hasher
}

// NewService 构造用户档案领域服务；db 与 hasher 均为装配方必须提供的硬依赖。
func NewService(db *database.DB, h *hasher.Hasher) *Service {
	if db == nil {
		panic("user: service requires a non-nil database")
	}
	if h == nil {
		panic("user: service requires a non-nil hasher")
	}
	return &Service{db: db, hasher: h}
}

// GetProfile 获取指定用户的档案视图。
func (s *Service) GetProfile(ctx context.Context, userID string) (Profile, error) {
	entity, err := s.Get(ctx, userID)
	if err != nil {
		return Profile{}, err
	}
	return ProjectProfile(entity), nil
}

// UpdateProfile 根据授权作用域执行档案局部更新与唯一性冲突消解。
func (s *Service) UpdateProfile(ctx context.Context, userID string, fields FieldSet, scope Scope) (Profile, error) {
	patch, err := s.clientPatch(ctx, userID, fields, scope)
	if err != nil {
		return Profile{}, err
	}
	if patch.Empty() {
		return s.GetProfile(ctx, userID)
	}

	entity, err := s.apply(ctx, userID, patch)
	if err != nil {
		return Profile{}, err
	}
	return ProjectProfile(entity), nil
}

// UpdateAdminUser 由管理员局部更新用户，含基础档案、角色、状态与封禁信息。
func (s *Service) UpdateAdminUser(ctx context.Context, userID string, fields AdminFieldSet) (*sqlc.User, error) {
	patch, err := PatchFromAdminFieldSet(fields)
	if err != nil {
		return nil, err
	}
	if err := s.checkUsername(ctx, userID, patch); err != nil {
		return nil, err
	}
	if patch.Empty() {
		return s.Get(ctx, userID)
	}
	return s.apply(ctx, userID, patch)
}

// List 分页查询用户列表并返回符合条件的总记录数（单查询取总数）。
func (s *Service) List(ctx context.Context, filter ListFilter, page, pageSize int) ([]sqlc.User, int, error) {
	params := filter.ListParams(page, pageSize)
	rows, err := s.db.Queries(ctx).ListUsers(ctx, params)
	if err != nil {
		return nil, 0, errs.Internal(err)
	}

	total := 0
	if len(rows) > 0 {
		total = int(rows[0].TotalCount)
	}
	users := lo.Map(rows, func(row sqlc.ListUsersRow, _ int) sqlc.User {
		return row.User
	})
	return users, total, nil
}

// ListChunk 供流式导出等批量场景按块读取用户列表。
func (s *Service) ListChunk(ctx context.Context, filter ListFilter, offset, limit int) ([]sqlc.User, error) {
	users, err := s.db.Queries(ctx).ListUsersChunk(ctx, filter.ChunkParams(offset, limit))
	if err != nil {
		return nil, errs.Internal(err)
	}
	return users, nil
}

// Get 根据主键 ID 获取单个用户实体，不存在时返回 404 语义错误。
func (s *Service) Get(ctx context.Context, id string) (*sqlc.User, error) {
	entity, err := s.db.Queries(ctx).GetUserByID(ctx, sqlc.GetUserByIDParams{ID: id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.NotFound("The requested user does not exist")
		}
		return nil, errs.Internal(err)
	}
	return &entity, nil
}

// Delete 软删除指定主键的用户。
func (s *Service) Delete(ctx context.Context, id string) error {
	affected, err := s.db.Queries(ctx).SoftDeleteUser(ctx, sqlc.SoftDeleteUserParams{ID: id})
	if err != nil {
		return errs.Internal(err)
	}
	if affected == 0 {
		return errs.NotFound("The requested user does not exist")
	}
	return nil
}

// clientPatch 生成客户端补丁，并按作用域剔除受控列。
func (s *Service) clientPatch(ctx context.Context, userID string, fields FieldSet, scope Scope) (Patch, error) {
	patch, err := PatchFromFieldSet(fields)
	if err != nil {
		return Patch{}, err
	}
	if scope == ScopeClient {
		// 客户端自助更新只允许基础档案字段，受控列在校验前即被丢弃。
		patch.EmailVerified = nil
		patch.Role = nil
		patch.Status = nil
		patch.Banned = nil
		patch.BanReason = nil
		patch.ClearBanReason = false
		patch.BanExpires = nil
		patch.ClearBanExpires = false
	}
	if err := s.checkUsername(ctx, userID, patch); err != nil {
		return Patch{}, err
	}
	return patch, nil
}

// checkUsername 在写入前预判用户名占用，唯一索引仍是最终仲裁。
func (s *Service) checkUsername(ctx context.Context, userID string, patch Patch) error {
	if patch.Username == nil || *patch.Username == "" {
		return nil
	}
	normalised := NormaliseUsername(*patch.Username)
	patch.Username = &normalised
	if patch.DisplayUsername == nil && !patch.ClearDisplayName {
		patch.DisplayUsername = &normalised
	}
	return EnsureUsernameAvailable(ctx, s.db, userID, normalised)
}

// apply 执行单语句局部更新，并把缺失记录与唯一冲突翻译为业务错误。
func (s *Service) apply(ctx context.Context, userID string, patch Patch) (*sqlc.User, error) {
	entity, err := s.db.Queries(ctx).UpdateUser(ctx, patch.Params(userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.NotFound("The requested user does not exist")
		}
		if _, ok := errs.As(err); ok {
			return nil, err
		}
		return nil, MapUniqueConflict(err)
	}
	return &entity, nil
}

// ListFilter 是用户列表查询的过滤条件。
type ListFilter struct {
	Search   string
	Status   enums.Status
	Role     enums.Role
	SortKey  string
	SortDesc bool
}

// NewListFilter 归一化过滤条件与排序表达式。
func NewListFilter(search string, status enums.Status, role enums.Role, sort string) ListFilter {
	key, desc := NormaliseSort(sort)
	return ListFilter{
		Search:   strings.TrimSpace(search),
		Status:   status,
		Role:     role,
		SortKey:  key,
		SortDesc: desc,
	}
}
