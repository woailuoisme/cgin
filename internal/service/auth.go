// Package service 汇集客户端用例：认证、档案、支付回调、实时通信与系统探针。
package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cgin/internal/config"
	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/internal/dto"
	"cgin/internal/enum"
	"cgin/internal/event"
	"cgin/internal/notification"
	appuser "cgin/internal/user"
	platformauth "cgin/pkg/authentication"
	"cgin/pkg/cache"
	"cgin/pkg/errs"
	"cgin/pkg/eventbus"
	"cgin/pkg/i18n"
	"cgin/pkg/logger"
	"cgin/pkg/mailer"
	platformNotification "cgin/pkg/notification"
)

// 邮件模板键，与 message/<语言>/mail.toml 中的键前缀一一对应。
const (
	mailKeyVerifyEmail = "verify_email"
	mailKeyResetLink   = "reset_link"
	mailKeyResetOTP    = "reset_otp"
)

// NotificationSender 定义外发通知的高层抽象契约。
type NotificationSender interface {
	Send(ctx context.Context, notifiables any, notif platformNotification.Notification, opts ...platformNotification.SendOption) error
}

// userStore 声明认证用例所需的用户账号与本地口令读写能力（接口由消费方定义）。
type userStore interface {
	Register(ctx context.Context, params appuser.RegisterParams) (*sqlc.User, error)
	Get(ctx context.Context, userID string) (*sqlc.User, error)
	ByEmail(ctx context.Context, email string) (*sqlc.User, error)
	ByUsername(ctx context.Context, username string) (*sqlc.User, error)
	MarkEmailVerified(ctx context.Context, userID string) (*sqlc.User, error)
	RecordLogin(ctx context.Context, userID, method string) error
	VerifyPassword(ctx context.Context, userID, password string) (bool, error)
	ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error
	ResetPassword(ctx context.Context, userID, newPassword string) error
}

// AuthDeps 是认证服务所需的依赖，由装配根注入。
type AuthDeps struct {
	DB         *database.DB
	Users      userStore
	Guard      *platformauth.Guard[*sqlc.User]
	Cache      cache.Store
	Mailer     mailer.Mailer
	Notifier   NotificationSender
	Translator i18n.Translator
	Config     *config.Config
	Publisher  eventbus.Publisher
}

// AuthService 实现认证用例。
type AuthService struct {
	users      userStore
	guard      *platformauth.Guard[*sqlc.User]
	mailer     mailer.Mailer
	notifier   NotificationSender
	translator i18n.Translator
	cfg        *config.Config
	verifier   *verifier
	publisher  eventbus.Publisher
}

// MustNewAuthService 构造认证服务；DB/Users/Guard 为装配方必须提供的硬依赖。
func MustNewAuthService(deps AuthDeps) *AuthService {
	if deps.DB == nil {
		panic("auth: service requires a non-nil DB")
	}
	if deps.Users == nil {
		panic("auth: service requires a non-nil Users")
	}
	if deps.Guard == nil {
		panic("auth: service requires a non-nil Guard")
	}
	return &AuthService{
		users:      deps.Users,
		guard:      deps.Guard,
		mailer:     deps.Mailer,
		notifier:   deps.Notifier,
		translator: deps.Translator,
		cfg:        deps.Config,
		verifier:   newVerifier(deps.DB, deps.Cache, deps.Config),
		publisher:  cmp.Or(deps.Publisher, eventbus.NopPublisher()),
	}
}

// SignUpEmail 注册邮箱密码账号，并直接签发登录会话。
func (s *AuthService) SignUpEmail(ctx context.Context, req dto.SignUpEmailBody) (dto.SessionResource, error) {
	created, err := s.users.Register(ctx, appuser.RegisterParams{
		Name:       req.Name,
		Email:      req.Email,
		Username:   req.Username,
		AvatarPath: req.AvatarPath,
		Password:   req.Password,
	})
	if err != nil {
		return dto.SessionResource{}, err
	}

	_ = s.publisher.Publish(ctx, &event.UserRegistered{
		UserID:       created.ID,
		Email:        created.Email,
		Name:         created.Name,
		RegisteredAt: time.Now(),
	})

	sess, err := s.guard.Login(ctx, created)
	if err != nil {
		return dto.SessionResource{}, err
	}
	s.recordLogin(ctx, created.ID, string(enum.ProviderCredential))

	return toSessionResource(sess, created), nil
}

// SignInEmail 邮箱密码登录。
func (s *AuthService) SignInEmail(ctx context.Context, req dto.SignInEmailBody) (dto.SessionResource, error) {
	user, err := s.users.ByEmail(ctx, req.Email)
	if err != nil {
		return dto.SessionResource{}, s.translateLookupFailure(ctx, err, req.Email)
	}
	return s.signInWithCredentials(ctx, user, req.Password)
}

// SignInUsername 用户名密码登录。
func (s *AuthService) SignInUsername(ctx context.Context, req dto.SignInUsernameBody) (dto.SessionResource, error) {
	user, err := s.users.ByUsername(ctx, req.Username)
	if err != nil {
		return dto.SessionResource{}, s.translateLookupFailure(ctx, err, req.Username)
	}
	return s.signInWithCredentials(ctx, user, req.Password)
}

// SignOut 作废当前访问令牌与可选的刷新令牌。
func (s *AuthService) SignOut(ctx context.Context, req dto.SignOutBody) error {
	if !s.guard.Check(ctx) {
		return errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	if req.RefreshToken != nil {
		return s.guard.Logout(ctx, *req.RefreshToken)
	}
	return s.guard.Logout(ctx)
}

// GetSession 返回当前用户资料。
func (s *AuthService) GetSession(ctx context.Context, userID string) (dto.GetSessionResource, error) {
	user, err := s.guard.ResolveUser(ctx, userID)
	if err != nil {
		return dto.GetSessionResource{}, err
	}
	return dto.GetSessionResource{User: dto.ProjectUser(user)}, nil
}

// Refresh 用刷新令牌轮换新的一对双令牌，旧刷新令牌立即作废。
func (s *AuthService) Refresh(ctx context.Context, req dto.RefreshTokenBody) (dto.SessionResource, error) {
	user, sess, err := s.guard.Refresh(ctx, req.RefreshToken)
	if err != nil {
		return dto.SessionResource{}, err
	}
	return toSessionResource(sess, user), nil
}

// SendVerificationOTP 发送邮箱验证码。
func (s *AuthService) SendVerificationOTP(ctx context.Context, language string, req dto.SendVerificationOTPBody) (dto.StatusResource, error) {
	user, err := s.users.ByEmail(ctx, req.Email)
	if err != nil {
		if isNotFound(err) {
			logger.FromContext(ctx).InfoContext(ctx, "verification requested for non-existent email, ignored")
			return dto.StatusResource{Status: true}, nil
		}
		return dto.StatusResource{}, err
	}
	if user.EmailVerified {
		return dto.StatusResource{Status: true}, nil
	}

	ttl := s.cfg.Auth.OTPTTL
	code, err := s.verifier.issueCode(ctx, purposeEmailVerify, user.Email, ttl)
	if err != nil {
		return dto.StatusResource{}, errs.Internal(err)
	}
	notif := notification.NewSignupVerificationNotification(user.Name, code)
	notif.ExpiresIn = ttl
	if err := s.notifyUser(ctx, language, user, notif, mailKeyVerifyEmail, code, ttl); err != nil {
		return dto.StatusResource{}, errs.Internal(err)
	}
	return dto.StatusResource{Status: true}, nil
}

// VerifyEmail 核销验证码并置位邮箱验证状态。
func (s *AuthService) VerifyEmail(ctx context.Context, req dto.VerifyEmailBody) (dto.StatusResource, error) {
	email := appuser.NormaliseEmail(req.Email)
	if err := s.verifier.consume(ctx, purposeEmailVerify, email, strings.TrimSpace(req.OTP)); err != nil {
		return dto.StatusResource{}, err
	}

	user, err := s.users.ByEmail(ctx, email)
	if err != nil {
		if isNotFound(err) {
			return dto.StatusResource{}, errs.NotFound("No account is associated with this email address")
		}
		return dto.StatusResource{}, err
	}
	if _, err := s.users.MarkEmailVerified(ctx, user.ID); err != nil {
		return dto.StatusResource{}, err
	}
	return dto.StatusResource{Status: true}, nil
}

func (s *AuthService) signInWithCredentials(ctx context.Context, user *sqlc.User, password string) (dto.SessionResource, error) {
	matched, err := s.users.VerifyPassword(ctx, user.ID, password)
	if err != nil {
		return dto.SessionResource{}, errs.Internal(err)
	}
	if !matched {
		return dto.SessionResource{}, errs.InvalidCredentials()
	}

	sess, err := s.guard.Login(ctx, user)
	if err != nil {
		return dto.SessionResource{}, err
	}
	s.recordLogin(ctx, user.ID, string(enum.ProviderCredential))
	return toSessionResource(sess, user), nil
}

func toSessionResource(sess platformauth.Session, user *sqlc.User) dto.SessionResource {
	return dto.SessionResource{
		Token:        sess.Token,
		RefreshToken: sess.RefreshToken,
		ExpiresIn:    sess.ExpiresIn,
		User:         dto.ProjectUser(user),
	}
}

func (s *AuthService) translateLookupFailure(ctx context.Context, err error, identifier string) error {
	if isNotFound(err) {
		if equaliser, ok := s.users.(platformauth.TimingEqualiser); ok {
			equaliser.EqualiseTiming(ctx, identifier)
		}
		logger.FromContext(ctx).InfoContext(ctx, "login failed: account not found", slog.String("identifier", identifier))
		return errs.InvalidCredentials()
	}
	return err
}

func isNotFound(err error) bool {
	bizErr, ok := errs.As(err)
	return ok && bizErr.Code() == errs.CodeNotFound
}

func (s *AuthService) recordLogin(ctx context.Context, userID, method string) {
	if err := s.users.RecordLogin(ctx, userID, method); err != nil {
		logger.FromContext(ctx).WarnContext(ctx, "failed to record last login",
			slog.Any("error", err),
			slog.String("user_id", userID),
		)
	}
}

func (s *AuthService) sendMail(ctx context.Context, language string, user *sqlc.User, mailKey, code string, ttl time.Duration) error {
	lang := s.translator.Language(language)
	vars := map[string]string{
		"Name": user.Name,
		"Code": code,
		"TTL":  strconv.Itoa(int(ttl.Minutes())),
		"Link": s.resetLink(user.Email, code),
	}

	subject, body := s.translator.Mail(lang, mailKey, vars)
	return s.mailer.Send(ctx, mailer.Message{To: []string{user.Email}, Subject: subject, Body: body})
}

func (s *AuthService) notifyUser(ctx context.Context, language string, user *sqlc.User, notif platformNotification.Notification, mailKey, code string, ttl time.Duration) error {
	if s.notifier != nil {
		return s.notifier.Send(ctx, appuser.Notifiable(user), notif)
	}
	return s.sendMail(ctx, language, user, mailKey, code, ttl)
}

// ChangePassword 校验当前口令后写入新口令。
func (s *AuthService) ChangePassword(ctx context.Context, userID string, req dto.ChangePasswordBody) error {
	if err := s.users.ChangePassword(ctx, userID, req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, appuser.ErrNoCredential) {
			return noCredentialError()
		}
		return err
	}
	// 密码变更成功后作废该用户在此之前签发的所有会话令牌
	_ = s.guard.InvalidateAll(ctx, userID)
	return nil
}

// ForgetPassword 发送携带一次性令牌的重置链接。
func (s *AuthService) ForgetPassword(ctx context.Context, language string, req dto.ForgetPasswordBody) (dto.StatusResource, error) {
	return s.sendResetCredential(ctx, language, purposeResetLink, req.Email)
}

// RequestPasswordReset 发送 6 位重置验证码。
func (s *AuthService) RequestPasswordReset(ctx context.Context, language string, req dto.RequestPasswordResetBody) (dto.StatusResource, error) {
	return s.sendResetCredential(ctx, language, purposeResetOTP, req.Email)
}

// ResetPassword 处理链接令牌与验证码两条互斥的重置路径。
func (s *AuthService) ResetPassword(ctx context.Context, req dto.ResetPasswordBody) (dto.StatusResource, error) {
	if err := appuser.ValidatePassword("newPassword", req.NewPassword); err != nil {
		return dto.StatusResource{}, err
	}

	user, err := s.resolveResetTarget(ctx, req)
	if err != nil {
		return dto.StatusResource{}, err
	}

	if err := s.users.ResetPassword(ctx, user.ID, req.NewPassword); err != nil {
		if errors.Is(err, appuser.ErrNoCredential) {
			return dto.StatusResource{}, noCredentialError()
		}
		return dto.StatusResource{}, err
	}

	_ = s.publisher.Publish(ctx, &event.PasswordChanged{
		UserID:    user.ID,
		ChangedAt: time.Now(),
	})

	// 重置密码成功后作废该用户在此之前签发的所有会话令牌
	_ = s.guard.InvalidateAll(ctx, user.ID)

	return dto.StatusResource{Status: true}, nil
}

// sendResetCredential 签发重置凭据并投递邮件。邮箱不存在时静默返回成功。
func (s *AuthService) sendResetCredential(ctx context.Context, language, purpose, email string) (dto.StatusResource, error) {
	user, err := s.users.ByEmail(ctx, email)
	if err != nil {
		if isNotFound(err) {
			logger.FromContext(ctx).InfoContext(ctx, "password reset requested for non-existent email, ignored")
			return dto.StatusResource{Status: true}, nil
		}
		return dto.StatusResource{}, err
	}

	code, mailKey, ttl, err := s.issueResetCredential(ctx, purpose, user.Email)
	if err != nil {
		return dto.StatusResource{}, errs.Internal(err)
	}
	notif := notification.NewPasswordResetNotification(user.Name, code)
	notif.ExpiresIn = ttl
	if err := s.notifyUser(ctx, language, user, notif, mailKey, code, ttl); err != nil {
		return dto.StatusResource{}, errs.Internal(err)
	}
	return dto.StatusResource{Status: true}, nil
}

// issueResetCredential 按用途签发重置凭据，返回明文、邮件模板键与寿命。
func (s *AuthService) issueResetCredential(ctx context.Context, purpose, email string) (code, mailKey string, ttl time.Duration, err error) {
	switch purpose {
	case purposeResetLink:
		ttl = s.cfg.Auth.PasswordResetTTL
		code, err = s.verifier.issueToken(ctx, purpose, email, ttl)
		mailKey = mailKeyResetLink
	case purposeResetOTP:
		ttl = s.cfg.Auth.OTPTTL
		code, err = s.verifier.issueCode(ctx, purpose, email, ttl)
		mailKey = mailKeyResetOTP
	default:
		err = errs.Internal(fmt.Errorf("unknown reset purpose %q", purpose))
	}
	return code, mailKey, ttl, err
}

// resolveResetTarget 核销凭据并定位待重置的账号。
func (s *AuthService) resolveResetTarget(ctx context.Context, req dto.ResetPasswordBody) (*sqlc.User, error) {
	email := appuser.NormaliseEmail(req.Email)

	purpose, credential := purposeResetOTP, ""
	switch {
	case req.Token != nil && strings.TrimSpace(*req.Token) != "":
		purpose, credential = purposeResetLink, strings.TrimSpace(*req.Token)
	case req.OTP != nil && strings.TrimSpace(*req.OTP) != "":
		credential = strings.TrimSpace(*req.OTP)
	default:
		return nil, errs.ValidationKeys(map[string][]errs.MessageKey{
			"token": {{Key: "token_or_code_required"}},
		})
	}

	if err := s.verifier.consume(ctx, purpose, email, credential); err != nil {
		return nil, err
	}

	user, err := s.users.ByEmail(ctx, email)
	if err != nil {
		if isNotFound(err) {
			return nil, errs.NotFound("No account is associated with this email address")
		}
		return nil, err
	}
	return user, nil
}

// resetLink 拼接前端重置页面完整地址。
func (s *AuthService) resetLink(email, token string) string {
	query := url.Values{"token": {token}, "email": {email}}
	return strings.TrimRight(s.cfg.App.URL, "/") + "/reset-password?" + query.Encode()
}

// noCredentialError 用于本地密码凭据缺失的场景。
func noCredentialError() error {
	return errs.NotFound("No password credential is associated with this account")
}
