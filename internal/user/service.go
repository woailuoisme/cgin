package user

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/samber/lo"

	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/internal/enum"
	"cgin/pkg/authentication"
	"cgin/pkg/errs"
	"cgin/pkg/hasher"
	"cgin/pkg/validator"
)

// Scope 声明档案更新的权限作用域。
type Scope string

const (
	// ScopeClient 声明客户端用户本人档案更新作用域，仅允许修改受限的展示档案字段。
	ScopeClient Scope = "client"
	// ScopeAdmin 声明管理端管理员更新作用域，具备完全字段可写权限。
	ScopeAdmin Scope = "admin"
)

// ErrNoCredential 表示用户没有本地口令凭据。
var ErrNoCredential = errors.New("user: no local password credential")

// dummyPasswordHash 是时序对齐用的固定哈希载荷，进程内只生成一次。
var dummyPasswordHash = sync.OnceValue(func() string {
	encoded, err := hasher.New().Make("cgin-login-timing-equaliser")
	if err != nil {
		return ""
	}
	return encoded
})

// RegisterParams 描述创建一个带本地口令凭据的用户所需的输入。
type RegisterParams struct {
	Name          string
	Email         string
	Username      *string
	AvatarPath    *string
	Role          enum.Role
	Status        enum.Status
	EmailVerified bool
	Password      string
}

// ValidatePassword 校验新口令强度，字段键与既有 422 明细保持一致。
func ValidatePassword(field, password string) error {
	return validator.PasswordStrength(field, password)
}

var (
	_ authentication.UserProvider[*sqlc.User] = (*Service)(nil)
	_ authentication.LoginRecorder            = (*Service)(nil)
	_ authentication.TimingEqualiser          = (*Service)(nil)
)

// Service 统一提供用户档案领域读写、本地口令凭据管理以及认证守卫 UserProvider 契约。
type Service struct {
	db     *database.DB
	hasher hasher.PasswordHasher
}

// MustNewService 构造用户领域服务；db 与 hasher 为装配方必须提供的硬依赖。
func MustNewService(db *database.DB, h hasher.PasswordHasher) *Service {
	if db == nil {
		panic("user: service requires a non-nil database")
	}
	if h == nil {
		panic("user: service requires a non-nil hasher")
	}
	return &Service{db: db, hasher: h}
}

// Identity 把用户行投影为令牌身份。
func (s *Service) Identity(u *sqlc.User) authentication.Identity {
	return Identity(u)
}

// FindByID 按主键查找用户主体；不存在时转换为 authentication.ErrUserNotFound。
func (s *Service) FindByID(ctx context.Context, id string) (*sqlc.User, error) {
	row, err := s.db.Queries(ctx).GetUserByID(ctx, sqlc.GetUserByIDParams{ID: id})
	if err != nil {
		return nil, mapProviderErr(err)
	}
	return &row, nil
}

// FindByCredentials 根据凭据中的账号标识检索主体（支持邮箱或用户名）。
func (s *Service) FindByCredentials(ctx context.Context, creds authentication.Credentials) (*sqlc.User, error) {
	q := s.db.Queries(ctx)
	account := strings.TrimSpace(creds.Account)
	if strings.Contains(account, "@") {
		row, err := q.GetUserByEmail(ctx, sqlc.GetUserByEmailParams{Email: NormaliseEmail(account)})
		if err != nil {
			return nil, mapProviderErr(err)
		}
		return &row, nil
	}

	row, err := q.GetUserByUsername(ctx, sqlc.GetUserByUsernameParams{Username: NormaliseUsername(account)})
	if err == nil {
		return &row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	fallback, err := q.GetUserByEmail(ctx, sqlc.GetUserByEmailParams{Email: NormaliseEmail(account)})
	if err != nil {
		return nil, mapProviderErr(err)
	}
	return &fallback, nil
}

// ValidateCredentials 比对主体本地密码哈希。
func (s *Service) ValidateCredentials(ctx context.Context, u *sqlc.User, creds authentication.Credentials) (bool, error) {
	if u == nil {
		return false, nil
	}
	return s.VerifyPassword(ctx, u.ID, creds.Password)
}

// EqualiseTiming 在账号不存在时执行等价 Argon2 校验，阻断基于响应耗时的账号枚举。
func (s *Service) EqualiseTiming(_ context.Context, secret string) {
	if s.hasher == nil {
		return
	}
	if encoded := dummyPasswordHash(); encoded != "" {
		_, _ = s.hasher.Check(encoded, secret)
	}
}

// VerifyPassword 校验指定用户的本地口令；凭据缺失按校验失败处理，避免泄漏账号状态。
func (s *Service) VerifyPassword(ctx context.Context, userID, password string) (bool, error) {
	if strings.TrimSpace(password) == "" {
		return false, nil
	}

	hash, err := s.credentialHash(ctx, userID)
	if errors.Is(err, ErrNoCredential) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.hasher.Check(*hash, password)
}

// ChangePassword 校验当前口令后原子更新为新口令。
func (s *Service) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	if err := validator.PasswordStrength("newPassword", newPassword); err != nil {
		return err
	}

	hash, err := s.credentialHash(ctx, userID)
	if err != nil {
		return err
	}
	matched, err := s.hasher.Check(*hash, currentPassword)
	if err != nil {
		return errs.Internal(err)
	}
	if !matched {
		return errs.PasswordMismatch()
	}
	return s.storePassword(ctx, userID, newPassword)
}

// ResetPassword 跳过当前口令校验，直接写入新口令。
func (s *Service) ResetPassword(ctx context.Context, userID, newPassword string) error {
	if err := validator.PasswordStrength("newPassword", newPassword); err != nil {
		return err
	}
	return s.storePassword(ctx, userID, newPassword)
}

// Register 在事务中原子创建用户与本地口令凭据，并统一归一化与默认值规则。
func (s *Service) Register(ctx context.Context, params RegisterParams) (*sqlc.User, error) {
	if err := validator.PasswordStrength("password", params.Password); err != nil {
		return nil, err
	}
	if s.hasher == nil {
		return nil, errs.Internal(errors.New("user: service hasher is not configured"))
	}

	passwordHash, err := s.hasher.Make(params.Password)
	if err != nil {
		return nil, errs.Internal(err)
	}

	createParams := sqlc.CreateUserParams{
		Name:          strings.TrimSpace(params.Name),
		Email:         NormaliseEmail(params.Email),
		EmailVerified: params.EmailVerified,
		AvatarPath:    params.AvatarPath,
		Role:          string(defaultRole(params.Role)),
		Status:        string(defaultStatus(params.Status)),
	}
	if params.Username != nil {
		username := NormaliseUsername(*params.Username)
		createParams.Username = &username
		createParams.DisplayUsername = &username
	}

	var created sqlc.User
	err = s.db.RunInTx(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		row, err := q.CreateUser(ctx, createParams)
		if err != nil {
			return err
		}
		if _, err := q.CreateAccount(ctx, sqlc.CreateAccountParams{
			UserID:     row.ID,
			AccountID:  row.ID,
			ProviderID: string(enum.ProviderCredential),
			Password:   &passwordHash,
		}); err != nil {
			return err
		}
		created = row
		return nil
	})
	if err != nil {
		return nil, MapUniqueConflict(err)
	}
	return &created, nil
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
	if err := s.checkUsername(ctx, userID, &patch); err != nil {
		return nil, err
	}
	if patch.Empty() {
		return s.Get(ctx, userID)
	}
	return s.apply(ctx, userID, patch)
}

// List 分页查询用户列表并返回符合条件的总记录数。
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

// ListAfter 供流式导出等批量场景按键集游标读取用户列表；空游标表示从头开始。
func (s *Service) ListAfter(ctx context.Context, filter ListFilter, afterID string, limit int) ([]sqlc.User, error) {
	users, err := s.db.Queries(ctx).ListUsersAfter(ctx, filter.AfterParams(afterID, limit))
	if err != nil {
		return nil, errs.Internal(err)
	}
	return users, nil
}

// Get 根据主键 ID 获取单个用户实体，不存在时返回 404 语义错误。
func (s *Service) Get(ctx context.Context, id string) (*sqlc.User, error) {
	entity, err := s.db.Queries(ctx).GetUserByID(ctx, sqlc.GetUserByIDParams{ID: id})
	if err != nil {
		return nil, translateLookup(err, "The requested user does not exist")
	}
	return &entity, nil
}

// ByEmail 按邮箱查找用户；不存在时返回 404 语义错误。
func (s *Service) ByEmail(ctx context.Context, email string) (*sqlc.User, error) {
	entity, err := s.db.Queries(ctx).GetUserByEmail(ctx, sqlc.GetUserByEmailParams{
		Email: NormaliseEmail(email),
	})
	if err != nil {
		return nil, translateLookup(err, "No account is associated with this email address")
	}
	return &entity, nil
}

// ByUsername 按登录用户名查找用户；不存在时返回 404 语义错误。
func (s *Service) ByUsername(ctx context.Context, username string) (*sqlc.User, error) {
	entity, err := s.db.Queries(ctx).GetUserByUsername(ctx, sqlc.GetUserByUsernameParams{
		Username: NormaliseUsername(username),
	})
	if err != nil {
		return nil, translateLookup(err, "No account is associated with this username")
	}
	return &entity, nil
}

// MarkEmailVerified 置位邮箱验证状态并返回最新用户行。
func (s *Service) MarkEmailVerified(ctx context.Context, userID string) (*sqlc.User, error) {
	entity, err := s.db.Queries(ctx).SetUserEmailVerified(ctx, sqlc.SetUserEmailVerifiedParams{ID: userID})
	if err != nil {
		return nil, translateLookup(err, "The requested user does not exist")
	}
	return &entity, nil
}

// RecordLogin 记录最近一次登录时间与方式。
func (s *Service) RecordLogin(ctx context.Context, userID, method string) error {
	if err := s.db.Queries(ctx).RecordUserLogin(ctx, sqlc.RecordUserLoginParams{
		ID:     userID,
		Method: &method,
	}); err != nil {
		return errs.Internal(err)
	}
	return nil
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

func (s *Service) credentialHash(ctx context.Context, userID string) (*string, error) {
	if s.hasher == nil {
		return nil, errs.Internal(errors.New("user: service hasher is not configured"))
	}

	hash, err := s.db.Queries(ctx).GetPasswordHash(ctx, sqlc.GetPasswordHashParams{
		UserID:     userID,
		ProviderID: string(enum.ProviderCredential),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoCredential
	}
	if err != nil {
		return nil, err
	}
	if hash == nil || *hash == "" {
		return nil, ErrNoCredential
	}
	return hash, nil
}

func (s *Service) storePassword(ctx context.Context, userID, password string) error {
	hash, err := s.hasher.Make(password)
	if err != nil {
		return errs.Internal(err)
	}

	affected, err := s.db.Queries(ctx).UpdateAccountPassword(ctx, sqlc.UpdateAccountPasswordParams{
		UserID:     userID,
		ProviderID: string(enum.ProviderCredential),
		Password:   &hash,
	})
	if err != nil {
		return errs.Internal(err)
	}
	if affected == 0 {
		return ErrNoCredential
	}
	return nil
}

func (s *Service) clientPatch(ctx context.Context, userID string, fields FieldSet, scope Scope) (Patch, error) {
	patch, err := PatchFromFieldSet(fields)
	if err != nil {
		return Patch{}, err
	}
	if scope == ScopeClient {
		patch.EmailVerified = nil
		patch.Role = nil
		patch.Status = nil
		patch.Banned = nil
		patch.BanReason = nil
		patch.ClearBanReason = false
		patch.BanExpires = nil
		patch.ClearBanExpires = false
	}
	if err := s.checkUsername(ctx, userID, &patch); err != nil {
		return Patch{}, err
	}
	return patch, nil
}

func (s *Service) checkUsername(ctx context.Context, userID string, patch *Patch) error {
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

func mapProviderErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return authentication.ErrUserNotFound
	}
	return err
}

func translateLookup(err error, notFound string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return errs.NotFound(notFound)
	}
	return errs.Internal(err)
}

func defaultRole(role enum.Role) enum.Role {
	if role == "" {
		return enum.RoleUser
	}
	return role
}

func defaultStatus(status enum.Status) enum.Status {
	if status == "" {
		return enum.StatusActive
	}
	return status
}
