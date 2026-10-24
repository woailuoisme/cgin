// Package auth 实现注册、登录、登出、令牌续签与密码重置等认证用例。
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/database"
	"bgin/internal/database/models"
	platformauth "bgin/internal/platform/authentication"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/eventbus"
	"bgin/internal/platform/hasher"
	"bgin/internal/platform/i18n"
	"bgin/internal/platform/logger"
	"bgin/internal/platform/mailer"
	"bgin/internal/platform/notification"
	"bgin/internal/platform/notification/contract"
	"bgin/internal/platform/validator"
	"bgin/internal/shared/events"
	shareduser "bgin/internal/shared/user"
)

// NotificationSender 定义外发通知的高层抽象契约。
type NotificationSender interface {
	Send(ctx context.Context, notifiables any, notif contract.Notification, opts ...notification.SendOption) error
}

// Deps 是认证服务所需的依赖，由 main 装配后注入。
type Deps struct {
	DB         *bun.DB
	Guard      *platformauth.Guard[*models.User]
	Tokens     *platformauth.TokenIssuer
	Hasher     *hasher.Hasher
	Cache      cache.Store
	Mailer     mailer.Mailer
	Notifier   NotificationSender
	Translator *i18n.Translator
	Config     *config.Config
	Publisher  eventbus.Publisher
}

// Service 实现认证用例。
type Service struct {
	db         *bun.DB
	guard      *platformauth.Guard[*models.User]
	hasher     *hasher.Hasher
	mailer     mailer.Mailer
	notifier   NotificationSender
	translator *i18n.Translator
	cfg        *config.Config
	verifier   *verifier
	publisher  eventbus.Publisher
}

// NewService 构造认证服务。
func NewService(deps Deps) *Service {
	guard := deps.Guard
	if guard == nil && deps.Tokens != nil && deps.DB != nil {
		policy := platformauth.PolicyFunc[*models.User](func(ctx context.Context, u *models.User) error {
			if err := shareduser.GuardState(u); err != nil {
				return err
			}
			if deps.Config != nil && deps.Config.Auth.RequireEmailVerification && !u.EmailVerified {
				return errs.New(errs.CodeAuthEmailNotVerified, "The email address has not been verified")
			}
			return nil
		})
		provider := shareduser.NewProvider(deps.DB, deps.Hasher)
		guard = platformauth.NewGuard(deps.Tokens, provider, policy)
	}
	return &Service{
		db:         deps.DB,
		guard:      guard,
		hasher:     deps.Hasher,
		mailer:     deps.Mailer,
		notifier:   deps.Notifier,
		translator: deps.Translator,
		cfg:        deps.Config,
		verifier:   newVerifier(deps.DB, deps.Cache, deps.Config),
		publisher:  deps.Publisher,
	}
}

// Guard 返回底层 Guard 实例。
func (s *Service) Guard() *platformauth.Guard[*models.User] { return s.guard }

// idb 获取上下文事务，若不存在则回退至服务默认数据库连接。
func (s *Service) idb(ctx context.Context) bun.IDB {
	return database.ResolveDB(ctx, s.db)
}

// SignUpEmail 注册邮箱密码账号，并按 Q15 的约定直接完成登录。
func (s *Service) SignUpEmail(ctx context.Context, req SignUpEmailRequest) (SessionResponse, error) {
	if err := validator.PasswordStrength("password", req.Password); err != nil {
		return SessionResponse{}, err
	}

	user := &models.User{
		Name:   strings.TrimSpace(req.Name),
		Email:  shareduser.NormaliseEmail(req.Email),
		Image:  req.Image,
		Role:   models.RoleUser,
		Status: models.StatusActive,
	}
	assignUsername(user, req.Username)

	hash, err := s.hasher.Make(req.Password)
	if err != nil {
		return SessionResponse{}, errs.Internal(err)
	}

	evt := &events.UserRegistered{
		UserID:       user.ID,
		Email:        user.Email,
		Name:         user.Name,
		RegisteredAt: time.Now(),
	}
	if err := s.db.RunInTx(ctx, nil, func(txCtx context.Context, tx bun.Tx) error {
		if err := models.CreateUserWithCredential(txCtx, &tx, user, hash); err != nil {
			return shareduser.MapUniqueConflict(err)
		}
		return nil
	}); err != nil {
		return SessionResponse{}, err
	}
	if s.publisher != nil {
		_ = s.publisher.Publish(ctx, evt)
	}

	session, err := s.issueSession(ctx, user)
	if err != nil {
		return SessionResponse{}, err
	}
	s.recordLogin(ctx, user.ID, models.ProviderCredential)

	return session, nil
}

// SignInEmail 邮箱密码登录。
func (s *Service) SignInEmail(ctx context.Context, req SignInEmailRequest) (SessionResponse, error) {
	user, err := models.FindUserByEmail(ctx, s.idb(ctx), req.Email)
	if err != nil {
		return SessionResponse{}, s.translateLookupFailure(ctx, err, req.Email)
	}
	return s.signInWithCredentials(ctx, user, req.Password)
}

// SignInUsername 用户名密码登录。
func (s *Service) SignInUsername(ctx context.Context, req SignInUsernameRequest) (SessionResponse, error) {
	user, err := models.FindUserByUsername(ctx, s.idb(ctx), req.Username)
	if err != nil {
		return SessionResponse{}, s.translateLookupFailure(ctx, err, req.Username)
	}
	return s.signInWithCredentials(ctx, user, req.Password)
}

// SignOut 作废当前访问令牌；同时传入刷新令牌时一并作废，否则续签通道仍然敞开。
func (s *Service) SignOut(ctx context.Context, req SignOutRequest) error {
	if req.RefreshToken != nil {
		return s.guard.Logout(ctx, *req.RefreshToken)
	}
	return s.guard.Logout(ctx)
}

// GetSession 返回当前用户。纯只读、不续签，令牌过期即 401（Q13）。
func (s *Service) GetSession(ctx context.Context, userID string) (GetSessionResponse, error) {
	user, err := s.guard.UserOrFail(ctx)
	if err != nil {
		var loadErr error
		user, loadErr = s.loadUser(ctx, userID)
		if loadErr != nil {
			return GetSessionResponse{}, loadErr
		}
	}
	return GetSessionResponse{User: projectUser(user)}, nil
}

// loadUser 兜底从数据库加载用户实体并进行状态校验。
func (s *Service) loadUser(ctx context.Context, userID string) (*models.User, error) {
	u, err := models.FindUserByID(ctx, s.idb(ctx), userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
		}
		return nil, errs.Internal(err)
	}
	if err := s.guardAccountState(u); err != nil {
		return nil, err
	}
	return u, nil
}

// Refresh 用刷新令牌换取新的一对令牌，旧的刷新令牌立即失效（轮换）。
func (s *Service) Refresh(ctx context.Context, req RefreshTokenRequest) (SessionResponse, error) {
	user, sess, err := s.guard.Refresh(ctx, req.RefreshToken)
	if err != nil {
		return SessionResponse{}, err
	}
	if err := s.guardAccountState(user); err != nil {
		return SessionResponse{}, err
	}

	return toSessionResponse(sess, user), nil
}

// ChangePassword 校验当前口令后写入新口令。
func (s *Service) ChangePassword(ctx context.Context, userID string, req ChangePasswordRequest) error {
	if err := validator.PasswordStrength("newPassword", req.NewPassword); err != nil {
		return err
	}

	account, err := models.FindCredentialAccount(ctx, s.idb(ctx), userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return noCredentialError()
		}
		return errs.Internal(err)
	}
	if account.Password == nil {
		return noCredentialError()
	}

	matched, err := s.hasher.Check(*account.Password, req.CurrentPassword)
	if err != nil {
		return errs.Internal(err)
	}
	if !matched {
		return errs.New(errs.CodeAuthPasswordMismatch, "The current password is incorrect")
	}

	hash, err := s.hasher.Make(req.NewPassword)
	if err != nil {
		return errs.Internal(err)
	}
	if err := models.UpdatePassword(ctx, s.idb(ctx), userID, hash); err != nil {
		return errs.Internal(err)
	}
	// 密码变更成功后作废该用户在此之前签发的所有会话令牌
	_ = s.guard.InvalidateAll(ctx, userID)
	return nil
}

// ForgetPassword 发送携带一次性令牌的重置链接。
func (s *Service) ForgetPassword(ctx context.Context, language string, req ForgetPasswordRequest) (StatusResponse, error) {
	return s.sendResetCredential(ctx, language, purposeResetLink, req.Email)
}

// RequestPasswordReset 发送 6 位重置验证码。
func (s *Service) RequestPasswordReset(ctx context.Context, language string, req RequestPasswordResetRequest) (StatusResponse, error) {
	return s.sendResetCredential(ctx, language, purposeResetOTP, req.Email)
}

// ResetPassword 处理链接令牌与验证码两条互斥的重置路径。
func (s *Service) ResetPassword(ctx context.Context, req ResetPasswordRequest) (StatusResponse, error) {
	if err := validator.PasswordStrength("newPassword", req.NewPassword); err != nil {
		return StatusResponse{}, err
	}

	user, err := s.resolveResetTarget(ctx, req)
	if err != nil {
		return StatusResponse{}, err
	}

	hash, err := s.hasher.Make(req.NewPassword)
	if err != nil {
		return StatusResponse{}, errs.Internal(err)
	}

	evt := &events.PasswordChanged{
		UserID:    user.ID,
		ChangedAt: time.Now(),
	}
	if err := s.db.RunInTx(ctx, nil, func(txCtx context.Context, tx bun.Tx) error {
		if err := models.UpdatePassword(txCtx, &tx, user.ID, hash); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return noCredentialError()
			}
			return errs.Internal(err)
		}
		return nil
	}); err != nil {
		return StatusResponse{}, err
	}
	if s.publisher != nil {
		_ = s.publisher.Publish(ctx, evt)
	}

	// 重置密码成功后作废该用户在此之前签发的所有会话令牌
	_ = s.guard.InvalidateAll(ctx, user.ID)

	return StatusResponse{Status: true}, nil
}

// SendVerificationOTP 发送邮箱验证码。
func (s *Service) SendVerificationOTP(ctx context.Context, language string, req SendVerificationOTPRequest) (StatusResponse, error) {
	user, err := models.FindUserByEmail(ctx, s.idb(ctx), req.Email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 静默成功：不向未认证调用方暴露邮箱是否已注册。
			logger.FromContext(ctx).Info("verification requested for non-existent email, ignored")
			return StatusResponse{Status: true}, nil
		}
		return StatusResponse{}, errs.Internal(err)
	}
	if user.EmailVerified {
		return StatusResponse{Status: true}, nil
	}

	ttl := s.cfg.Auth.OTPTTL
	code, err := s.verifier.issueCode(ctx, purposeEmailVerify, user.Email, ttl)
	if err != nil {
		return StatusResponse{}, errs.Internal(err)
	}
	notif := NewSignupVerificationNotification(user.Name, code)
	notif.ExpiresIn = ttl
	if err := s.notifyUser(ctx, language, user, notif, mailKeyVerifyEmail, code, ttl); err != nil {
		return StatusResponse{}, errs.Internal(err)
	}
	return StatusResponse{Status: true}, nil
}

// VerifyEmail 核销验证码并置位邮箱验证状态。
func (s *Service) VerifyEmail(ctx context.Context, req VerifyEmailRequest) (StatusResponse, error) {
	email := shareduser.NormaliseEmail(req.Email)
	if err := s.verifier.consume(ctx, purposeEmailVerify, email, strings.TrimSpace(req.OTP)); err != nil {
		return StatusResponse{}, err
	}

	user, err := models.FindUserByEmail(ctx, s.idb(ctx), email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StatusResponse{}, errs.NotFound("No account is associated with this email address")
		}
		return StatusResponse{}, errs.Internal(err)
	}
	if err := user.MarkEmailVerified(ctx, s.idb(ctx)); err != nil {
		return StatusResponse{}, errs.Internal(err)
	}
	return StatusResponse{Status: true}, nil
}

// 邮件模板键，与 lang/<语言>/mail.toml 中的键前缀一一对应。
const (
	mailKeyVerifyEmail = "verify_email"
	mailKeyResetLink   = "reset_link"
	mailKeyResetOTP    = "reset_otp"
)

// signInWithCredentials 校验本地密码凭据并签发令牌。
func (s *Service) signInWithCredentials(ctx context.Context, user *models.User, password string) (SessionResponse, error) {
	pwdHash, err := models.FindCredentialPassword(ctx, s.idb(ctx), user.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionResponse{}, errs.InvalidCredentials()
		}
		return SessionResponse{}, errs.Internal(err)
	}
	if pwdHash == nil || *pwdHash == "" {
		return SessionResponse{}, errs.InvalidCredentials()
	}

	matched, err := s.hasher.Check(*pwdHash, password)
	if err != nil {
		return SessionResponse{}, errs.Internal(err)
	}
	if !matched {
		return SessionResponse{}, errs.InvalidCredentials()
	}

	session, err := s.issueSession(ctx, user)
	if err != nil {
		return SessionResponse{}, err
	}
	s.recordLogin(ctx, user.ID, models.ProviderCredential)
	return session, nil
}

// issueSession 校验账号状态后签发双令牌并组装响应体，不写库。
func (s *Service) issueSession(ctx context.Context, user *models.User) (SessionResponse, error) {
	if err := s.guardAccountState(user); err != nil {
		return SessionResponse{}, err
	}

	sess, err := s.guard.Login(ctx, user)
	if err != nil {
		return SessionResponse{}, err
	}

	return toSessionResponse(sess, user), nil
}

func toSessionResponse(sess platformauth.Session, user *models.User) SessionResponse {
	return SessionResponse{
		Token:        sess.Token,
		RefreshToken: sess.RefreshToken,
		ExpiresIn:    sess.ExpiresIn,
		User:         projectUser(user),
	}
}

// guardAccountState 拒绝封禁、未激活与（开启开关时）未验证邮箱的账号。
func (s *Service) guardAccountState(user *models.User) error {
	if err := shareduser.GuardState(user); err != nil {
		return err
	}
	if s.cfg.Auth.RequireEmailVerification && !user.EmailVerified {
		return errs.New(errs.CodeAuthEmailNotVerified, "The email address has not been verified")
	}
	return nil
}

// translateLookupFailure 把"账号不存在"统一翻译为凭证错误，避免泄漏账号是否存在。
func (s *Service) translateLookupFailure(ctx context.Context, err error, identifier string) error {
	if errors.Is(err, sql.ErrNoRows) {
		s.equaliseLookupTiming(identifier)
		logger.FromContext(ctx).Info("login failed: account not found", zap.String("identifier", identifier))
		return errs.InvalidCredentials()
	}
	return errs.Internal(err)
}

// dummyPasswordHash 是时序对齐用的固定哈希载荷，进程内只生成一次。
var dummyPasswordHash = sync.OnceValue(func() string {
	encoded, err := hasher.New().Make("bgin-login-timing-equaliser")
	if err != nil {
		return ""
	}
	return encoded
})

// equaliseLookupTiming 在账号不存在时补做一次等价的口令校验。
// "密码错误"路径会执行 Argon2 校验而本路径不会，响应时间差足以枚举账号是否存在。
func (s *Service) equaliseLookupTiming(identifier string) {
	if s.hasher == nil {
		return
	}
	if encoded := dummyPasswordHash(); encoded != "" {
		_, _ = s.hasher.Check(encoded, identifier)
	}
}

// recordLogin 记录最近登录信息；失败不影响登录本身。
func (s *Service) recordLogin(ctx context.Context, userID, method string) {
	if err := models.RecordLogin(ctx, s.idb(ctx), userID, method); err != nil {
		logger.FromContext(ctx).Warn("failed to record last login", zap.Error(err), zap.String("user_id", userID))
	}
}

// sendResetCredential 签发重置凭据并投递邮件。邮箱不存在时静默返回成功。
func (s *Service) sendResetCredential(ctx context.Context, language, purpose, email string) (StatusResponse, error) {
	user, err := models.FindUserByEmail(ctx, s.idb(ctx), email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			logger.FromContext(ctx).Info("password reset requested for non-existent email, ignored")
			return StatusResponse{Status: true}, nil
		}
		return StatusResponse{}, errs.Internal(err)
	}

	code, mailKey, ttl, err := s.issueResetCredential(ctx, purpose, user.Email)
	if err != nil {
		return StatusResponse{}, errs.Internal(err)
	}
	notif := NewPasswordResetNotification(user.Name, code)
	notif.ExpiresIn = ttl
	if err := s.notifyUser(ctx, language, user, notif, mailKey, code, ttl); err != nil {
		return StatusResponse{}, errs.Internal(err)
	}
	return StatusResponse{Status: true}, nil
}

// issueResetCredential 按用途签发重置凭据，返回明文、邮件模板键与寿命。
func (s *Service) issueResetCredential(ctx context.Context, purpose, email string) (code, mailKey string, ttl time.Duration, err error) {
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
func (s *Service) resolveResetTarget(ctx context.Context, req ResetPasswordRequest) (*models.User, error) {
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

	user, err := models.FindUserByEmail(ctx, s.idb(ctx), email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errs.NotFound("No account is associated with this email address")
		}
		return nil, errs.Internal(err)
	}
	return user, nil
}

// sendMail 渲染并投递邮件；重置链接由前端站点根地址拼接而成。
func (s *Service) sendMail(ctx context.Context, language string, user *models.User, mailKey, code string, ttl time.Duration) error {
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
func (s *Service) resetLink(email, token string) string {
	query := url.Values{"token": {token}, "email": {email}}
	return strings.TrimRight(s.cfg.App.URL, "/") + "/reset-password?" + query.Encode()
}

// assignUsername 写入规范化后的用户名。
func assignUsername(user *models.User, username *string) {
	if username == nil {
		return
	}
	normalised := shareduser.NormaliseUsername(*username)
	user.Username = &normalised
	user.DisplayUsername = &normalised
}

// noCredentialError 用于本地密码凭据缺失的场景。
func noCredentialError() error {
	return errs.NotFound("No password credential is associated with this account")
}

func (s *Service) notifyUser(ctx context.Context, language string, user *models.User, notif contract.Notification, mailKey, code string, ttl time.Duration) error {
	if s.notifier != nil {
		return s.notifier.Send(ctx, user, notif)
	}
	return s.sendMail(ctx, language, user, mailKey, code, ttl)
}
