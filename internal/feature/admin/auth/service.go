package adminauth

import (
	"context"
	"errors"

	"cgin/internal/database/sqlc"
	appuser "cgin/internal/user"
	"cgin/pkg/authentication"
	"cgin/pkg/errs"
)

// passwordChanger 声明管理员改密所需的本地凭据能力（接口由消费方定义）。
type passwordChanger interface {
	ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error
}

// Session 封装管理员认证会话结果。
type Session struct {
	Token        string
	RefreshToken string
	ExpiresIn    int
	User         appuser.Profile
}

// AuthDeps 声明管理员认证服务的外部依赖。
type AuthDeps struct {
	Guard     *authentication.Guard[*sqlc.User]
	Passwords passwordChanger
}

// AuthService 实现后台管理员认证的业务逻辑。
type AuthService struct {
	guard     *authentication.Guard[*sqlc.User]
	passwords passwordChanger
}

// MustNewAuthService 构造 AuthService；Guard 与 Passwords 均为装配方必须提供的硬依赖。
func MustNewAuthService(deps AuthDeps) *AuthService {
	if deps.Guard == nil {
		panic("adminauth: service requires a non-nil Guard")
	}
	if deps.Passwords == nil {
		panic("adminauth: service requires a non-nil Passwords")
	}
	return &AuthService{guard: deps.Guard, passwords: deps.Passwords}
}

// SignInEmail 管理员邮箱密码登录。
func (s *AuthService) SignInEmail(ctx context.Context, email, password string) (Session, error) {
	u, sess, err := s.guard.Attempt(ctx, authentication.Credentials{
		Account:  email,
		Password: password,
	})
	if err != nil {
		if errors.Is(err, appuser.ErrRoleRestricted) {
			return Session{}, errs.InvalidCredentials()
		}
		return Session{}, err
	}

	return Session{
		Token:        sess.Token,
		RefreshToken: sess.RefreshToken,
		ExpiresIn:    sess.ExpiresIn,
		User:         appuser.ProjectProfile(u),
	}, nil
}

// GetSession 获取当前有效会话的管理员资料。
func (s *AuthService) GetSession(ctx context.Context, userID string) (appuser.Profile, error) {
	u, err := s.guard.ResolveUser(ctx, userID)
	if err != nil {
		return appuser.Profile{}, err
	}

	return appuser.ProjectProfile(u), nil
}

// Refresh 刷新管理员双令牌，实现令牌轮换。
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (Session, error) {
	u, sess, err := s.guard.Refresh(ctx, refreshToken)
	if err != nil {
		return Session{}, err
	}

	return Session{
		Token:        sess.Token,
		RefreshToken: sess.RefreshToken,
		ExpiresIn:    sess.ExpiresIn,
		User:         appuser.ProjectProfile(u),
	}, nil
}

// SignOut 登出并作废当前管理员访问令牌与可选的刷新令牌。
func (s *AuthService) SignOut(ctx context.Context, refreshToken *string) error {
	if !s.guard.Check(ctx) {
		return errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	if refreshToken != nil {
		return s.guard.Logout(ctx, *refreshToken)
	}
	return s.guard.Logout(ctx)
}

// ChangePassword 修改当前管理员登录密码。
func (s *AuthService) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	if _, err := s.guard.ResolveUser(ctx, userID); err != nil {
		return err
	}

	if err := s.passwords.ChangePassword(ctx, userID, currentPassword, newPassword); err != nil {
		if errors.Is(err, appuser.ErrNoCredential) {
			return errs.PasswordMismatch()
		}
		return err
	}

	_ = s.guard.InvalidateAll(ctx, userID)
	return nil
}
