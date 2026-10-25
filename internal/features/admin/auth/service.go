package adminauth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"cgin/internal/database/sqlc"
	"cgin/internal/platform/authentication"
	"cgin/internal/platform/errs"
	"cgin/internal/shared/enums"
	shareduser "cgin/internal/shared/user"
)

// passwordChanger 声明管理员改密所需的本地凭据能力（接口由消费方定义）。
type passwordChanger interface {
	ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error
}

// Deps 声明管理员认证服务的外部依赖。
type Deps struct {
	Guard     *authentication.Guard[*sqlc.User]
	Passwords passwordChanger
}

// Service 实现后台管理员认证的业务逻辑。
type Service struct {
	guard     *authentication.Guard[*sqlc.User]
	passwords passwordChanger
}

// MustNewService 构造 Service；Guard 与 Passwords 均为装配方必须提供的硬依赖。
func MustNewService(deps Deps) *Service {
	if deps.Guard == nil {
		panic("adminauth: service requires a non-nil Guard")
	}
	if deps.Passwords == nil {
		panic("adminauth: service requires a non-nil Passwords")
	}
	return &Service{guard: deps.Guard, passwords: deps.Passwords}
}

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
		loaded, loadErr := s.loadAdmin(ctx, userID)
		if loadErr != nil {
			return GetSessionResource{}, loadErr
		}
		user = loaded
	}

	return GetSessionResource{User: shareduser.ProjectProfile(user)}, nil
}

// loadAdmin 加载管理员实体并进行角色与状态校验。
func (s *Service) loadAdmin(ctx context.Context, userID string) (*sqlc.User, error) {
	if s.guard == nil || s.guard.Provider() == nil {
		return nil, errs.Internal(errors.New("adminauth: guard provider is not configured"))
	}

	u, err := s.guard.Provider().FindByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
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

func toSessionResource(sess authentication.Session, user *sqlc.User) SessionResource {
	return SessionResource{
		Token:        sess.Token,
		RefreshToken: sess.RefreshToken,
		ExpiresIn:    sess.ExpiresIn,
		User:         shareduser.ProjectProfile(user),
	}
}

// ChangePassword 修改当前管理员登录密码。
func (s *Service) ChangePassword(ctx context.Context, userID string, body ChangePasswordBody) error {
	// 新口令强度由凭据深模块统一校验；此处先做管理员语义校验，遵循"先授权、后入参"。
	if _, err := s.loadAdmin(ctx, userID); err != nil {
		return err
	}

	if err := s.passwords.ChangePassword(ctx, userID, body.CurrentPassword, body.NewPassword); err != nil {
		if errors.Is(err, shareduser.ErrNoCredential) {
			return errs.PasswordMismatch()
		}
		return err
	}

	// 密码变更成功后作废该管理员在此之前签发的所有会话令牌
	_ = s.guard.InvalidateAll(ctx, userID)
	return nil
}
