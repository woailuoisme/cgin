// Package service 汇集客户端用例：认证、档案、支付回调、实时通信与系统探针。
package service

import (
	"cmp"
	"context"
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
	"cgin/pkg/hasher"
	"cgin/pkg/i18n"
	"cgin/pkg/logger"
	"cgin/pkg/mailer"
	platformNotification "cgin/pkg/notification"
	"cgin/pkg/notification/contract"
)

// 邮件模板键，与 message/<语言>/mail.toml 中的键前缀一一对应。
const (
	mailKeyVerifyEmail = "verify_email"
	mailKeyResetLink   = "reset_link"
	mailKeyResetOTP    = "reset_otp"
)

// NotificationSender 定义外发通知的高层抽象契约。
type NotificationSender interface {
	Send(ctx context.Context, notifiables any, notif contract.Notification, opts ...platformNotification.SendOption) error
}

// credentialManager 声明认证用例所需的本地口令能力（接口由消费方定义）。
type credentialManager interface {
	VerifyPassword(ctx context.Context, userID, password string) (bool, error)
	ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error
	ResetPassword(ctx context.Context, userID, newPassword string) error
}

// userStore 声明认证用例所需的用户账号读写能力（接口由消费方定义）。
//
// 认证不再直接拼接 sqlc 查询：读写路径全部经由共享内核的用户 module，
// 用例因此可以脱离 PostgreSQL 被驱动。
type userStore interface {
	Register(ctx context.Context, params appuser.RegisterParams) (*sqlc.User, error)
	Get(ctx context.Context, userID string) (*sqlc.User, error)
	ByEmail(ctx context.Context, email string) (*sqlc.User, error)
	ByUsername(ctx context.Context, username string) (*sqlc.User, error)
	MarkEmailVerified(ctx context.Context, userID string) (*sqlc.User, error)
	RecordLogin(ctx context.Context, userID, method string) error
}

// AuthDeps 是认证服务所需的依赖，由 main 装配后注入。
type AuthDeps struct {
	DB          *database.DB
	Users       userStore
	Credentials credentialManager
	Guard       *platformauth.Guard[*sqlc.User]
	Hasher      hasher.PasswordHasher
	Cache       cache.Store
	Mailer      mailer.Mailer
	Notifier    NotificationSender
	Translator  i18n.Translator
	Config      *config.Config
	Publisher   eventbus.Publisher
}

// AuthService 实现认证用例。
type AuthService struct {
	users       userStore
	credentials credentialManager
	guard       *platformauth.Guard[*sqlc.User]
	hasher      hasher.PasswordHasher
	mailer      mailer.Mailer
	notifier    NotificationSender
	translator  i18n.Translator
	cfg         *config.Config
	verifier    *verifier
	publisher   eventbus.Publisher
}

// MustNewAuthService 构造认证服务；DB/Users/Credentials/Guard 为装配方必须提供的硬依赖。
func MustNewAuthService(deps AuthDeps) *AuthService {
	if deps.DB == nil {
		panic("auth: service requires a non-nil DB")
	}
	if deps.Users == nil {
		panic("auth: service requires a non-nil Users")
	}
	if deps.Credentials == nil {
		panic("auth: service requires a non-nil Credentials")
	}
	if deps.Guard == nil {
		panic("auth: service requires a non-nil Guard")
	}
	return &AuthService{
		users:       deps.Users,
		credentials: deps.Credentials,
		guard:       deps.Guard,
		hasher:      deps.Hasher,
		mailer:      deps.Mailer,
		notifier:    deps.Notifier,
		translator:  deps.Translator,
		cfg:         deps.Config,
		verifier:    newVerifier(deps.DB, deps.Cache, deps.Config),
		publisher:   cmp.Or(deps.Publisher, eventbus.NopPublisher()),
	}
}

// SignUpEmail 注册邮箱密码账号，并按 Q15 的约定直接完成登录。
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

	session, err := s.issueSession(ctx, created)
	if err != nil {
		return dto.SessionResource{}, err
	}
	s.recordLogin(ctx, created.ID, string(enum.ProviderCredential))

	return session, nil
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

// SignOut 作废当前访问令牌；同时传入刷新令牌时一并作废，否则续签通道仍然敞开。
func (s *AuthService) SignOut(ctx context.Context, req dto.SignOutBody) error {
	if !s.guard.Check(ctx) {
		return errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	if req.RefreshToken != nil {
		return s.guard.Logout(ctx, *req.RefreshToken)
	}
	return s.guard.Logout(ctx)
}

// GetSession 返回当前用户。纯只读、不续签，令牌过期即 401（Q13）。
func (s *AuthService) GetSession(ctx context.Context, userID string) (dto.GetSessionResource, error) {
	user, err := s.guard.UserOrFail(ctx)
	if err != nil {
		loaded, loadErr := s.loadUser(ctx, userID)
		if loadErr != nil {
			return dto.GetSessionResource{}, loadErr
		}
		user = loaded
	}
	return dto.GetSessionResource{User: dto.ProjectUser(user)}, nil
}

// loadUser 兜底从共享内核加载用户实体并进行状态校验。
func (s *AuthService) loadUser(ctx context.Context, userID string) (*sqlc.User, error) {
	u, err := s.users.Get(ctx, userID)
	if err != nil {
		if isNotFound(err) {
			return nil, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
		}
		return nil, err
	}
	if err := s.guardAccountState(u); err != nil {
		return nil, err
	}
	return u, nil
}

// Refresh 用刷新令牌换取新的一对令牌，旧的刷新令牌立即失效（轮换）。
func (s *AuthService) Refresh(ctx context.Context, req dto.RefreshTokenBody) (dto.SessionResource, error) {
	user, sess, err := s.guard.Refresh(ctx, req.RefreshToken)
	if err != nil {
		return dto.SessionResource{}, err
	}
	if err := s.guardAccountState(user); err != nil {
		return dto.SessionResource{}, err
	}

	return toSessionResource(sess, user), nil
}

// SendVerificationOTP 发送邮箱验证码。
func (s *AuthService) SendVerificationOTP(ctx context.Context, language string, req dto.SendVerificationOTPBody) (dto.StatusResource, error) {
	user, err := s.users.ByEmail(ctx, req.Email)
	if err != nil {
		if isNotFound(err) {
			// 静默成功：不向未认证调用方暴露邮箱是否已注册。
			logger.FromContext(ctx).Info().Msg("verification requested for non-existent email, ignored")
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

// signInWithCredentials 校验本地密码凭据并签发令牌。
func (s *AuthService) signInWithCredentials(ctx context.Context, user *sqlc.User, password string) (dto.SessionResource, error) {
	matched, err := s.credentials.VerifyPassword(ctx, user.ID, password)
	if err != nil {
		return dto.SessionResource{}, errs.Internal(err)
	}
	if !matched {
		return dto.SessionResource{}, errs.InvalidCredentials()
	}

	session, err := s.issueSession(ctx, user)
	if err != nil {
		return dto.SessionResource{}, err
	}
	s.recordLogin(ctx, user.ID, string(enum.ProviderCredential))
	return session, nil
}

// issueSession 校验账号状态后签发双令牌并组装响应体，不写库。
func (s *AuthService) issueSession(ctx context.Context, user *sqlc.User) (dto.SessionResource, error) {
	if err := s.guardAccountState(user); err != nil {
		return dto.SessionResource{}, err
	}

	sess, err := s.guard.Login(ctx, user)
	if err != nil {
		return dto.SessionResource{}, err
	}

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

// guardAccountState 拒绝封禁、未激活与（开启开关时）未验证邮箱的账号。
func (s *AuthService) guardAccountState(user *sqlc.User) error {
	if err := appuser.GuardState(user); err != nil {
		return err
	}
	if s.cfg.Auth.RequireEmailVerification && !user.EmailVerified {
		return errs.New(errs.CodeAuthEmailNotVerified, "The email address has not been verified")
	}
	return nil
}

// translateLookupFailure 把"账号不存在"统一翻译为凭证错误，避免泄漏账号是否存在。
func (s *AuthService) translateLookupFailure(ctx context.Context, err error, identifier string) error {
	if isNotFound(err) {
		if equaliser, ok := s.credentials.(platformauth.TimingEqualiser); ok {
			equaliser.EqualiseTiming(ctx, identifier)
		}
		logger.FromContext(ctx).Info().Str("identifier", identifier).Msg("login failed: account not found")
		return errs.InvalidCredentials()
	}
	return err
}

// isNotFound 报告错误是否为 404 语义（共享内核把底层空结果翻译后的稳定形式）。
func isNotFound(err error) bool {
	bizErr, ok := errs.As(err)
	return ok && bizErr.Code() == errs.CodeNotFound
}

// recordLogin 记录最近登录信息；失败不影响登录本身。
func (s *AuthService) recordLogin(ctx context.Context, userID, method string) {
	if err := s.users.RecordLogin(ctx, userID, method); err != nil {
		logger.FromContext(ctx).Warn().Err(err).Str("user_id", userID).Msg("failed to record last login")
	}
}

// sendMail 渲染并投递邮件；重置链接由前端站点根地址拼接而成。
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

func (s *AuthService) notifyUser(ctx context.Context, language string, user *sqlc.User, notif contract.Notification, mailKey, code string, ttl time.Duration) error {
	if s.notifier != nil {
		return s.notifier.Send(ctx, appuser.Notifiable(user), notif)
	}
	return s.sendMail(ctx, language, user, mailKey, code, ttl)
}
