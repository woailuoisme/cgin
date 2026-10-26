// Package service 汇集客户端用例：认证、档案、支付回调、实时通信与系统探针。
package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cgin/internal/config"
	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/internal/dto"
	platformauth "cgin/pkg/authentication"
	"cgin/pkg/cache"
	"cgin/pkg/errs"
	"cgin/pkg/eventbus"
	"cgin/pkg/hasher"
	"cgin/pkg/i18n"
	"cgin/pkg/logger"
	"cgin/pkg/mailer"
	"cgin/pkg/notification"
	"cgin/pkg/notification/contract"
	"cgin/shared/enums"
	"cgin/shared/events"
	shareduser "cgin/shared/user"
)

// 邮件模板键，与 messages/<语言>/mail.toml 中的键前缀一一对应。
const (
	mailKeyVerifyEmail = "verify_email"
	mailKeyResetLink   = "reset_link"
	mailKeyResetOTP    = "reset_otp"
)

// NotificationSender 定义外发通知的高层抽象契约。
type NotificationSender interface {
	Send(ctx context.Context, notifiables any, notif contract.Notification, opts ...notification.SendOption) error
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
	Register(ctx context.Context, params shareduser.RegisterParams) (*sqlc.User, error)
	Get(ctx context.Context, userID string) (*sqlc.User, error)
	ByEmail(ctx context.Context, email string) (*sqlc.User, error)
	ByUsername(ctx context.Context, username string) (*sqlc.User, error)
	MarkEmailVerified(ctx context.Context, userID string) (*sqlc.User, error)
	RecordLogin(ctx context.Context, userID, method string) error
}

// ClientAuthDeps 是认证服务所需的依赖，由 main 装配后注入。
type ClientAuthDeps struct {
	DB          *database.DB
	Users       userStore
	Credentials credentialManager
	Guard       *platformauth.Guard[*sqlc.User]
	Hasher      *hasher.Hasher
	Cache       cache.Store
	Mailer      mailer.Mailer
	Notifier    NotificationSender
	Translator  *i18n.Translator
	Config      *config.Config
	Publisher   eventbus.Publisher
}

// ClientAuthService 实现认证用例。
type ClientAuthService struct {
	users       userStore
	credentials credentialManager
	guard       *platformauth.Guard[*sqlc.User]
	hasher      *hasher.Hasher
	mailer      mailer.Mailer
	notifier    NotificationSender
	translator  *i18n.Translator
	cfg         *config.Config
	verifier    *verifier
	publisher   eventbus.Publisher
}

// MustNewClientAuthService 构造认证服务；DB/Users/Credentials/Guard 为装配方必须提供的硬依赖。
func MustNewClientAuthService(deps ClientAuthDeps) *ClientAuthService {
	if deps.DB == nil {
		panic("clientauth: service requires a non-nil DB")
	}
	if deps.Users == nil {
		panic("clientauth: service requires a non-nil Users")
	}
	if deps.Credentials == nil {
		panic("clientauth: service requires a non-nil Credentials")
	}
	if deps.Guard == nil {
		panic("clientauth: service requires a non-nil Guard")
	}
	return &ClientAuthService{
		users:       deps.Users,
		credentials: deps.Credentials,
		guard:       deps.Guard,
		hasher:      deps.Hasher,
		mailer:      deps.Mailer,
		notifier:    deps.Notifier,
		translator:  deps.Translator,
		cfg:         deps.Config,
		verifier:    newVerifier(deps.DB, deps.Cache, deps.Config),
		publisher:   deps.Publisher,
	}
}

// SignUpEmail 注册邮箱密码账号，并按 Q15 的约定直接完成登录。
func (s *ClientAuthService) SignUpEmail(ctx context.Context, req dto.SignUpEmailBody) (dto.SessionResource, error) {
	created, err := s.users.Register(ctx, shareduser.RegisterParams{
		Name:     req.Name,
		Email:    req.Email,
		Username: req.Username,
		Image:    req.Image,
		Password: req.Password,
	})
	if err != nil {
		return dto.SessionResource{}, err
	}

	if s.publisher != nil {
		_ = s.publisher.Publish(ctx, &events.UserRegistered{
			UserID:       created.ID,
			Email:        created.Email,
			Name:         created.Name,
			RegisteredAt: time.Now(),
		})
	}

	session, err := s.issueSession(ctx, created)
	if err != nil {
		return dto.SessionResource{}, err
	}
	s.recordLogin(ctx, created.ID, string(enums.ProviderCredential))

	return session, nil
}

// SignInEmail 邮箱密码登录。
func (s *ClientAuthService) SignInEmail(ctx context.Context, req dto.SignInEmailBody) (dto.SessionResource, error) {
	user, err := s.users.ByEmail(ctx, req.Email)
	if err != nil {
		return dto.SessionResource{}, s.translateLookupFailure(ctx, err, req.Email)
	}
	return s.signInWithCredentials(ctx, user, req.Password)
}

// SignInUsername 用户名密码登录。
func (s *ClientAuthService) SignInUsername(ctx context.Context, req dto.SignInUsernameBody) (dto.SessionResource, error) {
	user, err := s.users.ByUsername(ctx, req.Username)
	if err != nil {
		return dto.SessionResource{}, s.translateLookupFailure(ctx, err, req.Username)
	}
	return s.signInWithCredentials(ctx, user, req.Password)
}

// SignOut 作废当前访问令牌；同时传入刷新令牌时一并作废，否则续签通道仍然敞开。
func (s *ClientAuthService) SignOut(ctx context.Context, req dto.SignOutBody) error {
	if !s.guard.Check(ctx) {
		return errs.Unauthorized(errs.CodeAuthTokenInvalid, "Authentication is required")
	}
	if req.RefreshToken != nil {
		return s.guard.Logout(ctx, *req.RefreshToken)
	}
	return s.guard.Logout(ctx)
}

// GetSession 返回当前用户。纯只读、不续签，令牌过期即 401（Q13）。
func (s *ClientAuthService) GetSession(ctx context.Context, userID string) (dto.GetSessionResource, error) {
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
func (s *ClientAuthService) loadUser(ctx context.Context, userID string) (*sqlc.User, error) {
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
func (s *ClientAuthService) Refresh(ctx context.Context, req dto.RefreshTokenBody) (dto.SessionResource, error) {
	user, sess, err := s.guard.Refresh(ctx, req.RefreshToken)
	if err != nil {
		return dto.SessionResource{}, err
	}
	if err := s.guardAccountState(user); err != nil {
		return dto.SessionResource{}, err
	}

	return toSessionResource(sess, user), nil
}

// ChangePassword 校验当前口令后写入新口令。
func (s *ClientAuthService) ChangePassword(ctx context.Context, userID string, req dto.ChangePasswordBody) error {
	if err := s.credentials.ChangePassword(ctx, userID, req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, shareduser.ErrNoCredential) {
			return noCredentialError()
		}
		return err
	}
	// 密码变更成功后作废该用户在此之前签发的所有会话令牌
	_ = s.guard.InvalidateAll(ctx, userID)
	return nil
}

// ForgetPassword 发送携带一次性令牌的重置链接。
func (s *ClientAuthService) ForgetPassword(ctx context.Context, language string, req dto.ForgetPasswordBody) (dto.StatusResource, error) {
	return s.sendResetCredential(ctx, language, purposeResetLink, req.Email)
}

// RequestPasswordReset 发送 6 位重置验证码。
func (s *ClientAuthService) RequestPasswordReset(ctx context.Context, language string, req dto.RequestPasswordResetBody) (dto.StatusResource, error) {
	return s.sendResetCredential(ctx, language, purposeResetOTP, req.Email)
}

// ResetPassword 处理链接令牌与验证码两条互斥的重置路径。
func (s *ClientAuthService) ResetPassword(ctx context.Context, req dto.ResetPasswordBody) (dto.StatusResource, error) {
	if err := shareduser.ValidatePassword("newPassword", req.NewPassword); err != nil {
		return dto.StatusResource{}, err
	}

	user, err := s.resolveResetTarget(ctx, req)
	if err != nil {
		return dto.StatusResource{}, err
	}

	if err := s.credentials.ResetPassword(ctx, user.ID, req.NewPassword); err != nil {
		if errors.Is(err, shareduser.ErrNoCredential) {
			return dto.StatusResource{}, noCredentialError()
		}
		return dto.StatusResource{}, err
	}

	if s.publisher != nil {
		_ = s.publisher.Publish(ctx, &events.PasswordChanged{
			UserID:    user.ID,
			ChangedAt: time.Now(),
		})
	}

	// 重置密码成功后作废该用户在此之前签发的所有会话令牌
	_ = s.guard.InvalidateAll(ctx, user.ID)

	return dto.StatusResource{Status: true}, nil
}

// SendVerificationOTP 发送邮箱验证码。
func (s *ClientAuthService) SendVerificationOTP(ctx context.Context, language string, req dto.SendVerificationOTPBody) (dto.StatusResource, error) {
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
	notif := NewSignupVerificationNotification(user.Name, code)
	notif.ExpiresIn = ttl
	if err := s.notifyUser(ctx, language, user, notif, mailKeyVerifyEmail, code, ttl); err != nil {
		return dto.StatusResource{}, errs.Internal(err)
	}
	return dto.StatusResource{Status: true}, nil
}

// VerifyEmail 核销验证码并置位邮箱验证状态。
func (s *ClientAuthService) VerifyEmail(ctx context.Context, req dto.VerifyEmailBody) (dto.StatusResource, error) {
	email := shareduser.NormaliseEmail(req.Email)
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
func (s *ClientAuthService) signInWithCredentials(ctx context.Context, user *sqlc.User, password string) (dto.SessionResource, error) {
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
	s.recordLogin(ctx, user.ID, string(enums.ProviderCredential))
	return session, nil
}

// issueSession 校验账号状态后签发双令牌并组装响应体，不写库。
func (s *ClientAuthService) issueSession(ctx context.Context, user *sqlc.User) (dto.SessionResource, error) {
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
func (s *ClientAuthService) guardAccountState(user *sqlc.User) error {
	if err := shareduser.GuardState(user); err != nil {
		return err
	}
	if s.cfg.Auth.RequireEmailVerification && !user.EmailVerified {
		return errs.New(errs.CodeAuthEmailNotVerified, "The email address has not been verified")
	}
	return nil
}

// translateLookupFailure 把"账号不存在"统一翻译为凭证错误，避免泄漏账号是否存在。
func (s *ClientAuthService) translateLookupFailure(ctx context.Context, err error, identifier string) error {
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
func (s *ClientAuthService) recordLogin(ctx context.Context, userID, method string) {
	if err := s.users.RecordLogin(ctx, userID, method); err != nil {
		logger.FromContext(ctx).Warn().Err(err).Str("user_id", userID).Msg("failed to record last login")
	}
}

// sendResetCredential 签发重置凭据并投递邮件。邮箱不存在时静默返回成功。
func (s *ClientAuthService) sendResetCredential(ctx context.Context, language, purpose, email string) (dto.StatusResource, error) {
	user, err := s.users.ByEmail(ctx, email)
	if err != nil {
		if isNotFound(err) {
			logger.FromContext(ctx).Info().Msg("password reset requested for non-existent email, ignored")
			return dto.StatusResource{Status: true}, nil
		}
		return dto.StatusResource{}, err
	}

	code, mailKey, ttl, err := s.issueResetCredential(ctx, purpose, user.Email)
	if err != nil {
		return dto.StatusResource{}, errs.Internal(err)
	}
	notif := NewPasswordResetNotification(user.Name, code)
	notif.ExpiresIn = ttl
	if err := s.notifyUser(ctx, language, user, notif, mailKey, code, ttl); err != nil {
		return dto.StatusResource{}, errs.Internal(err)
	}
	return dto.StatusResource{Status: true}, nil
}

// issueResetCredential 按用途签发重置凭据，返回明文、邮件模板键与寿命。
func (s *ClientAuthService) issueResetCredential(ctx context.Context, purpose, email string) (code, mailKey string, ttl time.Duration, err error) {
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
func (s *ClientAuthService) resolveResetTarget(ctx context.Context, req dto.ResetPasswordBody) (*sqlc.User, error) {
	email := shareduser.NormaliseEmail(req.Email)

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

// sendMail 渲染并投递邮件；重置链接由前端站点根地址拼接而成。
func (s *ClientAuthService) sendMail(ctx context.Context, language string, user *sqlc.User, mailKey, code string, ttl time.Duration) error {
	lang := s.translator.Language(language)
	vars := map[string]string{
		"Name": user.Name,
		"Code": code,
		"TTL":  strconv.Itoa(int(ttl.Minutes())),
		"Link": s.resetLink(user.Email, code),
	}

	subject, body := s.translator.Mail(lang, mailKey, vars)
	return s.mailer.Send(ctx, mailer.Message{To: user.Email, Subject: subject, Body: body})
}

// resetLink 拼接前端重置页面完整地址。
func (s *ClientAuthService) resetLink(email, token string) string {
	query := url.Values{"token": {token}, "email": {email}}
	return strings.TrimRight(s.cfg.App.URL, "/") + "/reset-password?" + query.Encode()
}

// noCredentialError 用于本地密码凭据缺失的场景。
func noCredentialError() error {
	return errs.NotFound("No password credential is associated with this account")
}

func (s *ClientAuthService) notifyUser(ctx context.Context, language string, user *sqlc.User, notif contract.Notification, mailKey, code string, ttl time.Duration) error {
	if s.notifier != nil {
		return s.notifier.Send(ctx, shareduser.Notifiable(user), notif)
	}
	return s.sendMail(ctx, language, user, mailKey, code, ttl)
}
