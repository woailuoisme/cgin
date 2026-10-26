package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"cgin/internal/database/sqlc"
	"cgin/internal/dto"
	"cgin/internal/event"
	"cgin/internal/notification"
	appuser "cgin/internal/user"
	"cgin/pkg/errs"
	"cgin/pkg/logger"
)

// ChangePassword 校验当前口令后写入新口令。
func (s *AuthService) ChangePassword(ctx context.Context, userID string, req dto.ChangePasswordBody) error {
	if err := s.credentials.ChangePassword(ctx, userID, req.CurrentPassword, req.NewPassword); err != nil {
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

	if err := s.credentials.ResetPassword(ctx, user.ID, req.NewPassword); err != nil {
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
			logger.FromContext(ctx).Info().Msg("password reset requested for non-existent email, ignored")
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
