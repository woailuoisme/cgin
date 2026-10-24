package factories

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/uptrace/bun"

	"bgin/internal/database"
	"bgin/internal/database/models"
	"bgin/internal/platform/hasher"
)

// UserFactory 是面向 models.User 的领域工厂，提供开箱即用的假数据定义与状态链。
type UserFactory struct {
	base *Factory[models.User]
}

// NewUserFactory 构造用户模型工厂。db 可为 nil（纯内存纳秒级构建）。
func NewUserFactory(db bun.IDB) *UserFactory {
	return &UserFactory{
		base: New(db, func(f *gofakeit.Faker) *models.User {
			now := time.Now().UTC()
			id := newUUID()
			email := strings.ToLower(f.Email())
			username := strings.ToLower(f.Username())
			dispUser := f.Username()
			phone := f.Phone()

			return &models.User{
				Base: database.Base{
					ID:        id.String(),
					CreatedAt: now,
					UpdatedAt: now,
				},
				Name:                f.Name(),
				Email:               email,
				EmailVerified:       true,
				Role:                models.RoleUser,
				Status:              models.StatusActive,
				Banned:              false,
				TwoFactorEnabled:    false,
				Username:            &username,
				DisplayUsername:     &dispUser,
				PhoneNumber:         &phone,
				PhoneNumberVerified: true,
				IsAnonymous:         false,
			}
		}),
	}
}

// clone 产生当前 UserFactory 的深层不可变副本。
func (uf *UserFactory) clone() *UserFactory {
	return &UserFactory{
		base: uf.base.clone(),
	}
}

// Count 设置批量生成的数量，返回工厂副本。
func (uf *UserFactory) Count(count int) *UserFactory {
	cp := uf.clone()
	cp.base = cp.base.Count(count)
	return cp
}

// ChunkSize 设置批量持久化分块大小，返回工厂副本。
func (uf *UserFactory) ChunkSize(size int) *UserFactory {
	cp := uf.clone()
	cp.base = cp.base.ChunkSize(size)
	return cp
}

// OnProgress 注册批量持久化分块写入时的进度回调钩子，返回工厂副本。
func (uf *UserFactory) OnProgress(fn ProgressFunc) *UserFactory {
	cp := uf.clone()
	cp.base = cp.base.OnProgress(fn)
	return cp
}

// Seed 设置确定性随机数种子，返回工厂副本。
func (uf *UserFactory) Seed(seed uint64) *UserFactory {
	cp := uf.clone()
	cp.base = cp.base.Seed(seed)
	return cp
}

// State 附加自定义状态修改函数，返回工厂副本。
func (uf *UserFactory) State(fn StateFunc[models.User]) *UserFactory {
	cp := uf.clone()
	cp.base = cp.base.State(fn)
	return cp
}

// Sequence 附加基于下标的序列计算函数，返回工厂副本。
func (uf *UserFactory) Sequence(fn SequenceFunc[models.User]) *UserFactory {
	cp := uf.clone()
	cp.base = cp.base.Sequence(fn)
	return cp
}

// AfterMaking 注册内存构建完成后的回调，返回工厂副本。
func (uf *UserFactory) AfterMaking(fn HookFunc[models.User]) *UserFactory {
	cp := uf.clone()
	cp.base = cp.base.AfterMaking(fn)
	return cp
}

// AfterCreating 注册持久化完成后的回调，返回工厂副本。
func (uf *UserFactory) AfterCreating(fn HookFunc[models.User]) *UserFactory {
	cp := uf.clone()
	cp.base = cp.base.AfterCreating(fn)
	return cp
}

// Admin 将用户角色设置为系统管理员，返回工厂副本。
func (uf *UserFactory) Admin() *UserFactory {
	return uf.State(func(u *models.User) {
		u.Role = models.RoleAdmin
	})
}

// User 将用户角色重置为普通用户，返回工厂副本。
func (uf *UserFactory) User() *UserFactory {
	return uf.State(func(u *models.User) {
		u.Role = models.RoleUser
	})
}

// Inactive 将用户账号状态设为未激活，返回工厂副本。
func (uf *UserFactory) Inactive() *UserFactory {
	return uf.State(func(u *models.User) {
		u.Status = models.StatusInactive
	})
}

// Unverified 将用户邮箱与手机号设置为未验证状态，返回工厂副本。
func (uf *UserFactory) Unverified() *UserFactory {
	return uf.State(func(u *models.User) {
		u.EmailVerified = false
		u.PhoneNumberVerified = false
	})
}

// TwoFactor 启用双因素认证，返回工厂副本。
func (uf *UserFactory) TwoFactor() *UserFactory {
	return uf.State(func(u *models.User) {
		u.TwoFactorEnabled = true
	})
}

// Banned 将用户标记为封禁状态，返回工厂副本。
func (uf *UserFactory) Banned(reason string, expires *time.Time) *UserFactory {
	return uf.State(func(u *models.User) {
		u.Status = models.StatusBanned
		u.Banned = true
		u.BanReason = &reason
		u.BanExpires = expires
	})
}

// WithAvatar 为用户配置头像 URL，返回工厂副本。
func (uf *UserFactory) WithAvatar(avatarURL ...string) *UserFactory {
	return uf.State(func(u *models.User) {
		if len(avatarURL) > 0 && avatarURL[0] != "" {
			u.Image = &avatarURL[0]
			return
		}
		img := fmt.Sprintf("https://picsum.photos/seed/%s/200/200", uf.base.faker.UUID())
		u.Image = &img
	})
}

// WithAccount 级联创建本地凭据密码账号；password 传空串表示使用 FixturePassword。
// 该修饰器在 Create/CreateMany 持久化触发时，通过 AfterCreating 钩子自动向 account 表写入哈希凭据。
func (uf *UserFactory) WithAccount(password string) *UserFactory {
	rawPwd := cmp.Or(password, FixturePassword)

	return uf.AfterCreating(func(ctx context.Context, u *models.User) error {
		db := database.ResolveDB(ctx, uf.base.DB())
		if db == nil {
			return nil
		}

		pwdHasher := hasher.New()
		hashedPwd, err := pwdHasher.Make(rawPwd)
		if err != nil {
			return fmt.Errorf("factory with-account hash password failed: %w", err)
		}

		accountID := newUUID()
		now := time.Now().UTC()
		account := &models.Account{
			Base: database.Base{
				ID:        accountID.String(),
				CreatedAt: now,
				UpdatedAt: now,
			},
			UserID:     u.ID,
			AccountID:  u.Email,
			ProviderID: models.ProviderCredential,
			Password:   &hashedPwd,
		}

		if _, err := db.NewInsert().Model(account).Exec(ctx); err != nil {
			return fmt.Errorf("factory with-account insert account failed: %w", err)
		}
		return nil
	})
}

// Make 在内存中生成单个 User 实体（不写数据库）。
func (uf *UserFactory) Make(ctx context.Context) (*models.User, error) {
	return uf.base.Make(ctx)
}

// MakeMany 在内存中生成指定数量的 User 实体切片。
func (uf *UserFactory) MakeMany(ctx context.Context, counts ...int) ([]*models.User, error) {
	return uf.base.MakeMany(ctx, counts...)
}

// Create 在内存生成实体并持久化保存至数据库。
func (uf *UserFactory) Create(ctx context.Context) (*models.User, error) {
	return uf.base.Create(ctx)
}

// CreateMany 批量创建并持久化保存 User 实体切片。
func (uf *UserFactory) CreateMany(ctx context.Context, counts ...int) ([]*models.User, error) {
	return uf.base.CreateMany(ctx, counts...)
}

// Base 暴露底层泛型工厂基座。
func (uf *UserFactory) Base() *Factory[models.User] {
	return uf.base
}
