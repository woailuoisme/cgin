package clientauth

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
	"cgin/internal/enum"
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

// Session 描述业务用例层认证会话成果。
type Session struct {
	Token        string
	RefreshToken string
	ExpiresIn    int
	User         *sqlc.User
}

// SignUpParams 封装用户注册输入参数。
type SignUpParams struct {
	Name       string
	Email      string
	Password   string
	Username   *string
	AvatarPath *string
}

// ResetPasswordParams 封装密码重置输入参数。
type ResetPasswordParams struct {
	Email       string
	Token       *string
	OTP         *string
	NewPassword string
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
func (s *AuthService) SignUpEmail(ctx context.Context, params SignUpParams) (*Session, error) {
	created, err := s.users.Register(ctx, appuser.RegisterParams{
		Name:       params.Name,
		Email:      params.Email,
		Username:   params.Username,
		AvatarPath: params.AvatarPath,
		Password:   params.Password,
	})
	if err != nil {
		return nil, err
	}

	_ = s.publisher.Publish(ctx, &UserRegistered{
		UserID:       created.ID,
		Email:        created.Email,
		Name:         created.Name,
		RegisteredAt: time.Now(),
	})

	sess, err := s.guard.Login(ctx, created)
	if err != nil {
		return nil, err
	}
	s.recordLogin(ctx, created.ID, string(enum.ProviderCredential))

	return &Session{
		Token:        sess.Token,
		RefreshToken: sess.RefreshToken,
		ExpiresIn:    sess.ExpiresIn,
		User:         created,
	}, nil
}

// SignInEmail 邮箱密码登录。
func (s *AuthService) SignInEmail(ctx context.Context, email, password string) (*Session, error) {
	user, err := s.users.ByEmail(ctx, email)
	if err != nil {
		return nil, s.translateLookupFailure(ctx, err, email)
	}
	return s.signInWithCredentials(ctx, user, password)
}

// SignInUsername 用户名密码登录。
func (s *AuthService) SignInUsername(ctx context.Context, username, password string) (*Session, error) {
	user, err := s.users.ByUsername(ctx, username)
	if err != nil {
		return nil, s.translateLookupFailure(ctx, err, username)
	}
	return s.signInWithCredentials(ctx, user, password)
}

// SignOut 作废当前访问令牌与可选的刷新令牌。
func (s *AuthService) SignOut(ctx context.Context, refreshToken *string) error {
	if !s.guard.Check(ctx) {
		return errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	if refreshToken != nil {
		return s.guard.Logout(ctx, *refreshToken)
	}
	return s.guard.Logout(ctx)
}

// GetSession 返回当前用户数据模型。
func (s *AuthService) GetSession(ctx context.Context, userID string) (*sqlc.User, error) {
	return s.guard.ResolveUser(ctx, userID)
}

// Refresh 用刷新令牌轮换新的一对双令牌，旧刷新令牌立即作废。
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (*Session, error) {
	u, sess, err := s.guard.Refresh(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	return &Session{
		Token:        sess.Token,
		RefreshToken: sess.RefreshToken,
		ExpiresIn:    sess.ExpiresIn,
		User:         u,
	}, nil
}

// SendVerificationOTP 发送邮箱验证码。
func (s *AuthService) SendVerificationOTP(ctx context.Context, language, email string) error {
	u, err := s.users.ByEmail(ctx, email)
	if err != nil {
		if isNotFound(err) {
			logger.FromContext(ctx).InfoContext(ctx, "verification requested for non-existent email, ignored")
			return nil
		}
		return err
	}
	if u.EmailVerified {
		return nil
	}

	ttl := s.cfg.Auth.OTPTTL
	code, err := s.verifier.issueCode(ctx, purposeEmailVerify, u.Email, ttl)
	if err != nil {
		return errs.Internal(err)
	}
	notif := NewSignupVerificationNotification(u.Name, code)
	notif.ExpiresIn = ttl
	if err := s.notifyUser(ctx, language, u, notif, mailKeyVerifyEmail, code, ttl); err != nil {
		return errs.Internal(err)
	}
	return nil
}

// VerifyEmail 核销验证码并置位邮箱验证状态。
func (s *AuthService) VerifyEmail(ctx context.Context, rawEmail, otp string) error {
	email := appuser.NormaliseEmail(rawEmail)
	if err := s.verifier.consume(ctx, purposeEmailVerify, email, strings.TrimSpace(otp)); err != nil {
		return err
	}

	u, err := s.users.ByEmail(ctx, email)
	if err != nil {
		if isNotFound(err) {
			return errs.NotFound("No account is associated with this email address")
		}
		return err
	}
	if _, err := s.users.MarkEmailVerified(ctx, u.ID); err != nil {
		return err
	}
	return nil
}

func (s *AuthService) signInWithCredentials(ctx context.Context, u *sqlc.User, password string) (*Session, error) {
	matched, err := s.users.VerifyPassword(ctx, u.ID, password)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if !matched {
		return nil, errs.InvalidCredentials()
	}

	sess, err := s.guard.Login(ctx, u)
	if err != nil {
		return nil, err
	}
	s.recordLogin(ctx, u.ID, string(enum.ProviderCredential))
	return &Session{
		Token:        sess.Token,
		RefreshToken: sess.RefreshToken,
		ExpiresIn:    sess.ExpiresIn,
		User:         u,
	}, nil
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

func (s *AuthService) sendMail(ctx context.Context, language string, u *sqlc.User, mailKey, code string, ttl time.Duration) error {
	lang := s.translator.Language(language)
	vars := map[string]string{
		"Name": u.Name,
		"Code": code,
		"TTL":  strconv.Itoa(int(ttl.Minutes())),
		"Link": s.resetLink(u.Email, code),
	}

	subject, body := s.translator.Mail(lang, mailKey, vars)
	return s.mailer.Send(ctx, mailer.Message{To: []string{u.Email}, Subject: subject, Body: body})
}

func (s *AuthService) notifyUser(ctx context.Context, language string, u *sqlc.User, notif platformNotification.Notification, mailKey, code string, ttl time.Duration) error {
	if s.notifier != nil {
		return s.notifier.Send(ctx, appuser.Notifiable(u), notif)
	}
	return s.sendMail(ctx, language, u, mailKey, code, ttl)
}

// ChangePassword 校验当前口令后写入新口令。
func (s *AuthService) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	if err := s.users.ChangePassword(ctx, userID, currentPassword, newPassword); err != nil {
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
func (s *AuthService) ForgetPassword(ctx context.Context, language, email string) error {
	return s.sendResetCredential(ctx, language, purposeResetLink, email)
}

// RequestPasswordReset 发送 6 位重置验证码。
func (s *AuthService) RequestPasswordReset(ctx context.Context, language, email string) error {
	return s.sendResetCredential(ctx, language, purposeResetOTP, email)
}

// ResetPassword 处理链接令牌与验证码两条互斥的重置路径。
func (s *AuthService) ResetPassword(ctx context.Context, params ResetPasswordParams) error {
	if err := appuser.ValidatePassword("newPassword", params.NewPassword); err != nil {
		return err
	}

	u, err := s.resolveResetTarget(ctx, params)
	if err != nil {
		return err
	}

	if err := s.users.ResetPassword(ctx, u.ID, params.NewPassword); err != nil {
		if errors.Is(err, appuser.ErrNoCredential) {
			return noCredentialError()
		}
		return err
	}

	_ = s.publisher.Publish(ctx, &PasswordChanged{
		UserID:    u.ID,
		ChangedAt: time.Now(),
	})

	// 重置密码成功后作废该用户在此之前签发的所有会话令牌
	_ = s.guard.InvalidateAll(ctx, u.ID)

	return nil
}

// sendResetCredential 签发重置凭据并投递邮件。邮箱不存在时静默返回成功。
func (s *AuthService) sendResetCredential(ctx context.Context, language, purpose, email string) error {
	u, err := s.users.ByEmail(ctx, email)
	if err != nil {
		if isNotFound(err) {
			logger.FromContext(ctx).InfoContext(ctx, "password reset requested for non-existent email, ignored")
			return nil
		}
		return err
	}

	code, mailKey, ttl, err := s.issueResetCredential(ctx, purpose, u.Email)
	if err != nil {
		return errs.Internal(err)
	}
	notif := NewPasswordResetNotification(u.Name, code)
	notif.ExpiresIn = ttl
	if err := s.notifyUser(ctx, language, u, notif, mailKey, code, ttl); err != nil {
		return errs.Internal(err)
	}
	return nil
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
func (s *AuthService) resolveResetTarget(ctx context.Context, params ResetPasswordParams) (*sqlc.User, error) {
	email := appuser.NormaliseEmail(params.Email)

	purpose, credential := purposeResetOTP, ""
	switch {
	case params.Token != nil && strings.TrimSpace(*params.Token) != "":
		purpose, credential = purposeResetLink, strings.TrimSpace(*params.Token)
	case params.OTP != nil && strings.TrimSpace(*params.OTP) != "":
		credential = strings.TrimSpace(*params.OTP)
	default:
		return nil, errs.ValidationKeys(map[string][]errs.MessageKey{
			"token": {{Key: "token_or_code_required"}},
		})
	}

	if err := s.verifier.consume(ctx, purpose, email, credential); err != nil {
		return nil, err
	}

	u, err := s.users.ByEmail(ctx, email)
	if err != nil {
		if isNotFound(err) {
			return nil, errs.NotFound("No account is associated with this email address")
		}
		return nil, err
	}
	return u, nil
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
