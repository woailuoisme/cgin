package user

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/internal/platform/authentication"
	"cgin/internal/platform/hasher"
)

var (
	_ authentication.UserProvider[*sqlc.User] = (*Provider)(nil)
	_ authentication.LoginRecorder            = (*Provider)(nil)
)

// Provider 是认证守卫的用户主体与本地密码凭据适配器。
// 只做查询编排，不引入任何领域包装类型。
type Provider struct {
	db     *database.DB
	hasher *hasher.Hasher
}

// NewProvider 构造 Provider 实例；db 与 hasher 均为装配方必须提供的硬依赖。
func NewProvider(db *database.DB, h *hasher.Hasher) *Provider {
	if db == nil {
		panic("user: provider requires a non-nil database")
	}
	if h == nil {
		panic("user: provider requires a non-nil hasher")
	}
	return &Provider{db: db, hasher: h}
}

// Identity 把用户行投影为令牌身份。
func (p *Provider) Identity(u *sqlc.User) authentication.Identity {
	return Identity(u)
}

// FindByID 按主键查找用户主体。
func (p *Provider) FindByID(ctx context.Context, id string) (*sqlc.User, error) {
	row, err := p.db.Queries(ctx).GetUserByID(ctx, sqlc.GetUserByIDParams{ID: id})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindByCredentials 根据凭据中的账号标识检索主体（支持邮箱或用户名）。
func (p *Provider) FindByCredentials(ctx context.Context, creds authentication.Credentials) (*sqlc.User, error) {
	q := p.db.Queries(ctx)
	account := strings.TrimSpace(creds.Account)
	if strings.Contains(account, "@") {
		row, err := q.GetUserByEmail(ctx, sqlc.GetUserByEmailParams{Email: NormaliseEmail(account)})
		if err != nil {
			return nil, err
		}
		return &row, nil
	}

	row, err := q.GetUserByUsername(ctx, sqlc.GetUserByUsernameParams{Username: NormaliseUsername(account)})
	if err == nil {
		return &row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	fallback, err := q.GetUserByEmail(ctx, sqlc.GetUserByEmailParams{Email: NormaliseEmail(account)})
	if err != nil {
		return nil, err
	}
	return &fallback, nil
}

// ValidateCredentials 比对主体本地密码哈希。
func (p *Provider) ValidateCredentials(ctx context.Context, u *sqlc.User, creds authentication.Credentials) (bool, error) {
	if u == nil {
		return false, nil
	}
	return p.VerifyPassword(ctx, u.ID, creds.Password)
}

// RecordLogin 记录用户登录成功的时间与方式。
func (p *Provider) RecordLogin(ctx context.Context, id string, method string) error {
	return p.db.Queries(ctx).RecordUserLogin(ctx, sqlc.RecordUserLoginParams{ID: id, Method: &method})
}
