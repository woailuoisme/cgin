package user

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/uptrace/bun"

	"bgin/internal/database/models"
	"bgin/internal/platform/authentication"
	"bgin/internal/platform/hasher"
)

var (
	_ authentication.UserProvider[*models.User] = (*Provider)(nil)
	_ authentication.LoginRecorder              = (*Provider)(nil)
)

// Provider 实现了 authentication.UserProvider[*models.User] 与 authentication.LoginRecorder，
// 负责作为 Guard 的用户获取与凭据核验适配器。
type Provider struct {
	db     *bun.DB
	hasher *hasher.Hasher
}

// NewProvider 构造 Provider 实例。
func NewProvider(db *bun.DB, h *hasher.Hasher) *Provider {
	return &Provider{db: db, hasher: h}
}

// WithHasher 链式注入或覆盖 Hasher。
func (p *Provider) WithHasher(h *hasher.Hasher) *Provider {
	p.hasher = h
	return p
}

// DB 返回底层数据库句柄。
func (p *Provider) DB() *bun.DB {
	return p.db
}

// Hasher 返回底层密码哈希器。
func (p *Provider) Hasher() *hasher.Hasher {
	return p.hasher
}

// FindByID 按主键查找用户主体。
func (p *Provider) FindByID(ctx context.Context, id string) (*models.User, error) {
	return models.FindUserByID(ctx, p.db, id)
}

// FindByCredentials 根据凭据中的账号标识检索主体（支持邮箱或用户名检索）。
func (p *Provider) FindByCredentials(ctx context.Context, creds authentication.Credentials) (*models.User, error) {
	account := strings.TrimSpace(creds.Account)
	if strings.Contains(account, "@") {
		return models.FindUserByEmail(ctx, p.db, account)
	}
	user, err := models.FindUserByUsername(ctx, p.db, account)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return models.FindUserByEmail(ctx, p.db, account)
}

// ValidateCredentials 校验指定主体是否与凭证匹配（比对密码哈希）。
func (p *Provider) ValidateCredentials(ctx context.Context, u *models.User, creds authentication.Credentials) (bool, error) {
	if u == nil || strings.TrimSpace(creds.Password) == "" {
		return false, nil
	}
	pwdHash, err := models.FindCredentialPassword(ctx, p.db, u.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if pwdHash == nil || *pwdHash == "" {
		return false, nil
	}
	if p.hasher == nil {
		return false, errors.New("user: provider hasher is not configured")
	}
	return p.hasher.Check(*pwdHash, creds.Password)
}

// RecordLogin 记录用户登录成功的时间与方式。
func (p *Provider) RecordLogin(ctx context.Context, id string, method string) error {
	return (&models.User{ID: id}).RecordLogin(ctx, p.db, method)
}

// FindCredentialAccount 查找指定用户的本地凭据账户。
func (p *Provider) FindCredentialAccount(ctx context.Context, userID string) (*models.Account, error) {
	return models.FindCredentialAccount(ctx, p.db, userID)
}

// UpdatePassword 更新指定用户的本地凭据密码哈希。
func (p *Provider) UpdatePassword(ctx context.Context, userID, passwordHash string) error {
	return models.UpdatePassword(ctx, p.db, userID, passwordHash)
}
