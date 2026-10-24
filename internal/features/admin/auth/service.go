package adminauth

import (
	"context"
	"database/sql"
	"errors"

	"github.com/uptrace/bun"

	"bgin/internal/database/models"
	"bgin/internal/platform/authentication"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/hasher"
	"bgin/internal/platform/validator"
	"bgin/internal/shared/enums"
	shareduser "bgin/internal/shared/user"
)

// Deps 声明管理员认证服务的外部依赖。
type Deps struct {
	DB     *bun.DB
	Guard  *authentication.Guard[*models.User]
	Tokens *authentication.TokenIssuer
	Hasher *hasher.Hasher
}

// Service 实现后台管理员认证的业务逻辑。
type Service struct {
	db     bun.IDB
	guard  *authentication.Guard[*models.User]
	hasher *hasher.Hasher
}

// NewService 构造 Service。
func NewService(deps Deps) *Service {
	guard := deps.Guard
	if guard == nil && deps.Tokens != nil && deps.DB != nil {
		provider := shareduser.NewProvider(deps.DB, deps.Hasher)
		guard = authentication.NewGuard(deps.Tokens, provider, shareduser.AdminPolicy{})
	}
	return &Service{
		db:     deps.DB,
		guard:  guard,
		hasher: deps.Hasher,
	}
}

// Guard 返回底层 Guard 实例。
func (s *Service) Guard() *authentication.Guard[*models.User] { return s.guard }

// SignInEmail 管理员邮箱密码登录。
func (s *Service) SignInEmail(ctx context.Context, body SignInEmailBody) (SessionResource, error) {
	user, sess, err := s.guard.Attempt(ctx, authentication.Credentials{
		Account:  body.Email,
		Password: body.Password,
	})
	if err != nil {
		if errors.Is(err, shareduser.ErrRoleRestricted) {
			return SessionResource{}, errs.InvalidCredentials()
		}
		return SessionResource{}, err
	}

	return toSessionResource(sess, user), nil
}

// GetSession 获取当前有效会话的管理员资料。
func (s *Service) GetSession(ctx context.Context, userID string) (GetSessionResource, error) {
	user, err := s.guard.UserOrFail(ctx)
	if err != nil {
		var loadErr error
		user, loadErr = s.loadAdmin(ctx, userID)
		if loadErr != nil {
			return GetSessionResource{}, loadErr
		}
	}

	return GetSessionResource{User: shareduser.ProjectProfile(user)}, nil
}

// loadAdmin 兜底加载管理员实体并进行状态校验。
func (s *Service) loadAdmin(ctx context.Context, userID string) (*models.User, error) {
	var u *models.User
	var err error
	switch {
	case s.guard != nil && s.guard.Provider() != nil:
		u, err = s.guard.Provider().FindByID(ctx, userID)
	case s.db != nil:
		u, err = models.FindUserByID(ctx, s.db, userID)
	default:
		return nil, errors.New("adminauth: neither guard provider nor db is configured")
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
		}
		return nil, errs.Internal(err)
	}
	if u.Role != enums.RoleAdmin {
		return nil, errs.Forbidden("Access restricted to administrators")
	}
	if err := shareduser.GuardState(u); err != nil {
		return nil, err
	}
	return u, nil
}

// Refresh 刷新管理员双令牌，实现令牌轮换。
func (s *Service) Refresh(ctx context.Context, body RefreshTokenBody) (SessionResource, error) {
	user, sess, err := s.guard.Refresh(ctx, body.RefreshToken)
	if err != nil {
		return SessionResource{}, err
	}

	return toSessionResource(sess, user), nil
}

// SignOut 登出并作废当前管理员访问令牌与可选的刷新令牌。
func (s *Service) SignOut(ctx context.Context, body SignOutBody) error {
	if body.RefreshToken != nil {
		return s.guard.Logout(ctx, *body.RefreshToken)
	}
	return s.guard.Logout(ctx)
}

func toSessionResource(sess authentication.Session, user *models.User) SessionResource {
	return SessionResource{
		Token:        sess.Token,
		RefreshToken: sess.RefreshToken,
		ExpiresIn:    sess.ExpiresIn,
		User:         shareduser.ProjectProfile(user),
	}
}

type credentialManager interface {
	FindCredentialAccount(ctx context.Context, userID string) (*models.Account, error)
	UpdatePassword(ctx context.Context, userID, passwordHash string) error
}

func (s *Service) credentialManager() credentialManager {
	if s.guard != nil && s.guard.Provider() != nil {
		if cm, ok := s.guard.Provider().(credentialManager); ok {
			return cm
		}
	}
	return nil
}

func (s *Service) findCredentialAccount(ctx context.Context, userID string) (*models.Account, error) {
	if cm := s.credentialManager(); cm != nil {
		return cm.FindCredentialAccount(ctx, userID)
	}
	if s.db != nil {
		return models.FindCredentialAccount(ctx, s.db, userID)
	}
	return nil, sql.ErrNoRows
}

func (s *Service) updatePassword(ctx context.Context, userID, passwordHash string) error {
	if cm := s.credentialManager(); cm != nil {
		return cm.UpdatePassword(ctx, userID, passwordHash)
	}
	if s.db != nil {
		return models.UpdatePassword(ctx, s.db, userID, passwordHash)
	}
	return sql.ErrNoRows
}

var errPasswordMismatch = errs.New(errs.CodeAuthPasswordMismatch, "The current password is incorrect")

// ChangePassword 修改当前管理员登录密码。
func (s *Service) ChangePassword(ctx context.Context, userID string, body ChangePasswordBody) error {
	if err := validator.PasswordStrength("newPassword", body.NewPassword); err != nil {
		return err
	}

	if _, err := s.loadAdmin(ctx, userID); err != nil {
		return err
	}

	account, err := s.findCredentialAccount(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errPasswordMismatch
		}
		return errs.Internal(err)
	}
	if account.Password == nil {
		return errPasswordMismatch
	}

	matched, err := s.hasher.Check(*account.Password, body.CurrentPassword)
	if err != nil {
		return errs.Internal(err)
	}
	if !matched {
		return errPasswordMismatch
	}

	hash, err := s.hasher.Make(body.NewPassword)
	if err != nil {
		return errs.Internal(err)
	}

	if err := s.updatePassword(ctx, userID, hash); err != nil {
		return errs.Internal(err)
	}

	// 密码变更成功后作废该管理员在此之前签发的所有会话令牌
	_ = s.guard.InvalidateAll(ctx, userID)
	return nil
}
