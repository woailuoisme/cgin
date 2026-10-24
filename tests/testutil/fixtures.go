// Package testutil 提供跨测试包共享的测试辅助工具与测试对象母体（Fixture Builders）。
package testutil

import (
	"time"

	"github.com/google/uuid"

	"bgin/internal/database"
	"bgin/internal/database/models"
	"bgin/internal/shared/enums"
)

// UserFixtureBuilder 负责以链式 DSL 构造结构合法的 User 测试实体。
type UserFixtureBuilder struct {
	user *models.User
}

// User 返回初始化好的用户测试数据构建器，拥有合法的默认活跃状态与随机唯一标识。
func User() *UserFixtureBuilder {
	now := time.Now().UTC()
	uid := uuid.New().String()
	email := "user-" + uid[:8] + "@example.com"
	phone := "+8613800" + uid[:6]

	return &UserFixtureBuilder{
		user: &models.User{
			Base: database.Base{
				ID:        uid,
				CreatedAt: now,
				UpdatedAt: now,
			},
			Name:                "Test User",
			Email:               email,
			EmailVerified:       true,
			Role:                enums.RoleUser,
			Status:              enums.StatusActive,
			Banned:              false,
			TwoFactorEnabled:    false,
			PhoneNumber:         &phone,
			PhoneNumberVerified: true,
			IsAnonymous:         false,
		},
	}
}

// WithID 自定义用户主键 ID。
func (b *UserFixtureBuilder) WithID(id string) *UserFixtureBuilder {
	b.user.ID = id
	return b
}

// WithName 自定义用户名称。
func (b *UserFixtureBuilder) WithName(name string) *UserFixtureBuilder {
	b.user.Name = name
	return b
}

// WithEmail 自定义用户邮箱。
func (b *UserFixtureBuilder) WithEmail(email string) *UserFixtureBuilder {
	b.user.Email = email
	return b
}

// WithPhone 自定义用户手机号。
func (b *UserFixtureBuilder) WithPhone(phone string) *UserFixtureBuilder {
	b.user.PhoneNumber = &phone
	b.user.PhoneNumberVerified = true
	return b
}

// Admin 将用户角色提升为管理员。
func (b *UserFixtureBuilder) Admin() *UserFixtureBuilder {
	b.user.Role = enums.RoleAdmin
	return b
}

// Inactive 将用户状态设置为未激活。
func (b *UserFixtureBuilder) Inactive() *UserFixtureBuilder {
	b.user.Status = enums.StatusInactive
	return b
}

// Unverified 将用户邮箱和手机号设置为未验证状态。
func (b *UserFixtureBuilder) Unverified() *UserFixtureBuilder {
	b.user.EmailVerified = false
	b.user.PhoneNumberVerified = false
	return b
}

// Banned 标记用户为封禁状态并附带原因与可选过期时间。
func (b *UserFixtureBuilder) Banned(reason string, expires ...time.Time) *UserFixtureBuilder {
	b.user.Banned = true
	b.user.Status = enums.StatusBanned
	b.user.BanReason = &reason
	if len(expires) > 0 && !expires[0].IsZero() {
		exp := expires[0].UTC()
		b.user.BanExpires = &exp
	}
	return b
}

// TwoFactor 启用或禁用双因素认证。
func (b *UserFixtureBuilder) TwoFactor(enabled ...bool) *UserFixtureBuilder {
	val := true
	if len(enabled) > 0 {
		val = enabled[0]
	}
	b.user.TwoFactorEnabled = val
	return b
}

// Deleted 标记用户为软删除状态。
func (b *UserFixtureBuilder) Deleted(deletedAt ...time.Time) *UserFixtureBuilder {
	now := time.Now().UTC()
	if len(deletedAt) > 0 && !deletedAt[0].IsZero() {
		now = deletedAt[0].UTC()
	}
	b.user.DeletedAt = &now
	return b
}

// Build 返回构造好的 User 实体指针。
func (b *UserFixtureBuilder) Build() *models.User {
	return b.user
}

// BuildValue 返回构造好的 User 实体值类型。
func (b *UserFixtureBuilder) BuildValue() models.User {
	return *b.user
}

// AccountFixtureBuilder 负责以链式 DSL 构造结构合法的 Account 测试实体。
type AccountFixtureBuilder struct {
	account *models.Account
}

// Account 返回初始化好的账号认证凭据测试构建器。
func Account() *AccountFixtureBuilder {
	now := time.Now().UTC()
	aid := uuid.New().String()
	hashedPwd := "$argon2id$v=19$m=65536,t=3,p=4$mock_salt$mock_hash"

	return &AccountFixtureBuilder{
		account: &models.Account{
			Base: database.Base{
				ID:        aid,
				CreatedAt: now,
				UpdatedAt: now,
			},
			UserID:     uuid.New().String(),
			AccountID:  "acc-" + aid[:8] + "@example.com",
			ProviderID: models.ProviderCredential,
			Password:   &hashedPwd,
		},
	}
}

// WithID 自定义凭据 ID。
func (b *AccountFixtureBuilder) WithID(id string) *AccountFixtureBuilder {
	b.account.ID = id
	return b
}

// WithUserID 关联用户 ID。
func (b *AccountFixtureBuilder) WithUserID(userID string) *AccountFixtureBuilder {
	b.account.UserID = userID
	return b
}

// WithProvider 自定义提供商标识。
func (b *AccountFixtureBuilder) WithProvider(provider string) *AccountFixtureBuilder {
	b.account.ProviderID = provider
	return b
}

// WithAccountID 自定义账号登录标识符（如邮箱或三方 OpenID）。
func (b *AccountFixtureBuilder) WithAccountID(accountID string) *AccountFixtureBuilder {
	b.account.AccountID = accountID
	return b
}

// WithPassword 自定义哈希后密码。
func (b *AccountFixtureBuilder) WithPassword(hashedPwd string) *AccountFixtureBuilder {
	b.account.Password = &hashedPwd
	return b
}

// Build 返回构造好的 Account 实体指针。
func (b *AccountFixtureBuilder) Build() *models.Account {
	return b.account
}

// BuildValue 返回构造好的 Account 实体值类型。
func (b *AccountFixtureBuilder) BuildValue() models.Account {
	return *b.account
}
