package user

import (
	"context"
	"database/sql"
	"errors"

	"github.com/uptrace/bun"

	"bgin/internal/database/models"
	"bgin/internal/platform/errs"
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
	db *bun.DB
}

// NewService 构造用户档案领域服务。
func NewService(db *bun.DB) *Service {
	return &Service{db: db}
}

// GetProfile 获取指定用户的档案视图。
func (s *Service) GetProfile(ctx context.Context, userID string) (Profile, error) {
	entity, err := s.Get(ctx, userID)
	if err != nil {
		return Profile{}, err
	}
	return ProjectProfile(entity), nil
}

// UpdateProfile 根据授权作用域执行档案的三态局部更新与唯一性冲突消解。
func (s *Service) UpdateProfile(ctx context.Context, userID string, patch FieldSet, scope Scope) (Profile, error) {
	fields, err := CollectFields(ctx, s.db, userID, patch)
	if err != nil {
		return Profile{}, err
	}

	if scope == ScopeClient {
		// 客户端自助更新剔除全部受控列：判定依据就是仓储的可写白名单，
		// 不再另存一份"客户端禁写列"清单（两份清单必然漂移）。
		for column := range fields {
			if !writableColumns[column] {
				delete(fields, column)
			}
		}
	}

	if err := ApplyUpdate(ctx, s.db, userID, fields); err != nil {
		return Profile{}, err
	}

	return s.GetProfile(ctx, userID)
}

// UpdateAdminUser 由管理员局部更新用户，含基础档案、角色、状态与封禁信息。
func (s *Service) UpdateAdminUser(ctx context.Context, userID string, patch AdminFieldSet) (*models.User, error) {
	fields, err := CollectAdminFields(ctx, s.db, userID, patch)
	if err != nil {
		return nil, err
	}

	if err := ApplyUpdate(ctx, s.db, userID, fields); err != nil {
		return nil, err
	}

	return s.Get(ctx, userID)
}

// List 分页查询用户列表并返回符合条件的总记录数。
// 内部深度整合泛型 Query[User].Paginate，当符合条件的总数为 0 时自动触发 O(1) 短路，免除二次实体检索。
func (s *Service) List(ctx context.Context, filter ListFilter, page, pageSize int) ([]models.User, int, error) {
	users, total, err := models.UserQueryFrom(ctx, s.db).
		Scope(models.ScopeUserFilter(filter)).
		Scope(models.ScopeUserSort(filter.Sort)).
		Paginate(ctx, page, pageSize)
	if err != nil {
		return nil, 0, errs.Internal(err)
	}
	return users, total, nil
}

// ListChunk 供流式导出等批量场景按块读取用户列表。
func (s *Service) ListChunk(ctx context.Context, filter ListFilter, offset, limit int) ([]models.User, error) {
	users, err := models.UserQueryFrom(ctx, s.db).
		Scope(models.ScopeUserFilter(filter)).
		Scope(models.ScopeUserSort(filter.Sort)).
		Offset(offset).
		Limit(limit).
		Get(ctx)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return users, nil
}

// Get 根据主键 ID 获取单个用户实体，不存在时返回 404 语义错误。
func (s *Service) Get(ctx context.Context, id string) (*models.User, error) {
	entity, err := models.FindUserByID(ctx, s.db, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errs.NotFound("The requested user does not exist")
		}
		return nil, errs.Internal(err)
	}
	return entity, nil
}

// CreateWithCredential 在事务中原子创建用户实体与其口令凭据。
func (s *Service) CreateWithCredential(ctx context.Context, entity *models.User, passwordHash string) (*models.User, error) {
	if err := models.CreateUserWithCredential(ctx, s.db, entity, passwordHash); err != nil {
		return nil, MapUniqueConflict(err)
	}
	return entity, nil
}

// Delete 软删除指定主键的用户。
func (s *Service) Delete(ctx context.Context, id string) error {
	entity, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := entity.Delete(ctx, s.db); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errs.NotFound("The requested user does not exist")
		}
		return errs.Internal(err)
	}
	return nil
}
