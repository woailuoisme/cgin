package factories

import (
	"context"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/uptrace/bun"

	"bgin/internal/database"
	"bgin/internal/database/models"
	"bgin/internal/platform/hasher"
)

// AccountFactory 是面向 models.Account 凭据实体的领域工厂。
type AccountFactory struct {
	base *Factory[models.Account]
}

// NewAccountFactory 构造账号凭据工厂。
func NewAccountFactory(db bun.IDB) *AccountFactory {
	return &AccountFactory{
		base: New(db, func(f *gofakeit.Faker) *models.Account {
			now := time.Now().UTC()
			id := newUUID()
			userID := newUUID()
			email := strings.ToLower(f.Email())
			pwdHasher := hasher.New()
			hashedPwd := hashPasswordOrPanic(pwdHasher, FixturePassword)

			return &models.Account{
				Base: database.Base{
					ID:        id.String(),
					CreatedAt: now,
					UpdatedAt: now,
				},
				UserID:     userID.String(),
				AccountID:  email,
				ProviderID: models.ProviderCredential,
				Password:   &hashedPwd,
			}
		}),
	}
}

// clone 产生深层不可变副本。
func (af *AccountFactory) clone() *AccountFactory {
	return &AccountFactory{
		base: af.base.clone(),
	}
}

// Count 设置批量生成的数量，返回工厂副本。
func (af *AccountFactory) Count(count int) *AccountFactory {
	cp := af.clone()
	cp.base = cp.base.Count(count)
	return cp
}

// Seed 设置确定性随机数种子，返回工厂副本。
func (af *AccountFactory) Seed(seed uint64) *AccountFactory {
	cp := af.clone()
	cp.base = cp.base.Seed(seed)
	return cp
}

// State 附加自定义状态修改函数，返回工厂副本。
func (af *AccountFactory) State(fn StateFunc[models.Account]) *AccountFactory {
	cp := af.clone()
	cp.base = cp.base.State(fn)
	return cp
}

// ForUser 绑定该凭据归属的用户 ID 与账号标识（通常为邮箱）。
func (af *AccountFactory) ForUser(userID, accountID string) *AccountFactory {
	return af.State(func(a *models.Account) {
		a.UserID = userID
		a.AccountID = accountID
	})
}

// Password 设置自定义明文密码（自动通过 Argon2id 重新哈希）。
func (af *AccountFactory) Password(rawPassword string) *AccountFactory {
	return af.State(func(a *models.Account) {
		pwdHasher := hasher.New()
		hashedPwd := hashPasswordOrPanic(pwdHasher, rawPassword)
		a.Password = &hashedPwd
	})
}

// OAuth 将凭据转为第三方 OAuth 模式（清空本地哈希密码，填充 AccessToken 等）。
func (af *AccountFactory) OAuth(providerID, accountID string) *AccountFactory {
	return af.State(func(a *models.Account) {
		a.ProviderID = providerID
		a.AccountID = accountID
		a.Password = nil
		token := af.base.faker.UUID()
		a.AccessToken = &token
	})
}

// Make 在内存中生成单个 Account 实体。
func (af *AccountFactory) Make(ctx context.Context) (*models.Account, error) {
	return af.base.Make(ctx)
}

// MakeMany 在内存中生成多个 Account 实体。
func (af *AccountFactory) MakeMany(ctx context.Context, counts ...int) ([]*models.Account, error) {
	return af.base.MakeMany(ctx, counts...)
}

// Create 在内存生成实体并持久化保存至数据库。
func (af *AccountFactory) Create(ctx context.Context) (*models.Account, error) {
	return af.base.Create(ctx)
}

// CreateMany 批量创建并持久化保存 Account 实体切片。
func (af *AccountFactory) CreateMany(ctx context.Context, counts ...int) ([]*models.Account, error) {
	return af.base.CreateMany(ctx, counts...)
}
