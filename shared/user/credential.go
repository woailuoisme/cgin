package user

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"cgin/internal/database/sqlc"
	"cgin/pkg/errs"
	"cgin/pkg/validator"
	"cgin/shared/enums"
)

// ErrNoCredential 表示用户没有本地口令凭据。
var ErrNoCredential = errors.New("user: no local password credential")

// ValidatePassword 校验新口令强度，字段键与既有 422 明细保持一致。
func ValidatePassword(field, password string) error {
	return validator.PasswordStrength(field, password)
}

// VerifyPassword 校验指定用户的本地口令；凭据缺失按校验失败处理，避免泄漏账号状态。
func (p *Provider) VerifyPassword(ctx context.Context, userID, password string) (bool, error) {
	if strings.TrimSpace(password) == "" {
		return false, nil
	}

	hash, err := p.credentialHash(ctx, userID)
	if errors.Is(err, ErrNoCredential) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return p.hasher.Check(*hash, password)
}

// ChangePassword 校验当前口令后原子更新为新口令。
func (p *Provider) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	if err := validator.PasswordStrength("newPassword", newPassword); err != nil {
		return err
	}

	hash, err := p.credentialHash(ctx, userID)
	if err != nil {
		return err
	}
	matched, err := p.hasher.Check(*hash, currentPassword)
	if err != nil {
		return errs.Internal(err)
	}
	if !matched {
		return errs.PasswordMismatch()
	}
	return p.storePassword(ctx, userID, newPassword)
}

// ResetPassword 跳过当前口令校验，直接写入新口令。
func (p *Provider) ResetPassword(ctx context.Context, userID, newPassword string) error {
	if err := validator.PasswordStrength("newPassword", newPassword); err != nil {
		return err
	}
	return p.storePassword(ctx, userID, newPassword)
}

// credentialHash 读取本地口令哈希；缺失、为空或未配置哈希器时返回明确错误。
func (p *Provider) credentialHash(ctx context.Context, userID string) (*string, error) {
	if p.hasher == nil {
		return nil, errs.Internal(errors.New("user: provider hasher is not configured"))
	}

	hash, err := p.db.Queries(ctx).GetPasswordHash(ctx, sqlc.GetPasswordHashParams{
		UserID:     userID,
		ProviderID: string(enums.ProviderCredential),
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

// storePassword 哈希并写入新口令；凭据行缺失时返回 ErrNoCredential。
func (p *Provider) storePassword(ctx context.Context, userID, password string) error {
	hash, err := p.hasher.Make(password)
	if err != nil {
		return errs.Internal(err)
	}

	affected, err := p.db.Queries(ctx).UpdateAccountPassword(ctx, sqlc.UpdateAccountPasswordParams{
		UserID:     userID,
		ProviderID: string(enums.ProviderCredential),
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
