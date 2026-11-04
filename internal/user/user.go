// Package user 提供用户领域内核：档案视图投影、准入策略、唯一约束映射及账号与本地口令服务。
package user

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/internal/enum"
	"cgin/pkg/authentication"
	"cgin/pkg/errs"
	"cgin/pkg/notification"
)

// 唯一索引名，用于把冲突翻译成具体业务错误码。
const (
	indexUserEmail       = "idx_user_email"
	indexUserUsername    = "idx_user_username"
	indexUserPhoneNumber = "idx_user_phone_number"
)

// ErrRoleRestricted 声明非目标角色访问受限错误（403 Forbidden）。
var ErrRoleRestricted = errs.Forbidden("Access restricted to administrators")

// Profile 是用户档案视图：客户端与管理端共用的唯一字段来源。
type Profile struct {
	// 用户唯一标识
	ID string `json:"id" example:"usr_01J9X4N8Y0ABCD"`
	// 用户展示昵称
	Name string `json:"name" example:"Alex Mercer"`
	// 用户电子邮箱
	Email string `json:"email" example:"alex@example.com"`
	// 邮箱是否已验证
	EmailVerified bool `json:"emailVerified" example:"true"`
	// 用户头像在对象存储中的相对路径
	AvatarPath *string `json:"avatarPath" example:"avatars/20261031/uuid.webp"`
	// 用户权限角色 (user: 普通用户, admin: 系统管理员)
	Role enum.Role `json:"role" example:"user" enums:"user,admin"`
	// 唯一登录用户名
	Username *string `json:"username" example:"alexmercer"`
	// 用户显示名称
	DisplayUsername *string `json:"displayUsername" example:"Alex Mercer"`
	// E.164 格式手机号
	PhoneNumber *string `json:"phoneNumber" example:"+8613800138000"`
	// 账号创建时间 (ISO 8601 UTC)
	CreatedAt time.Time `json:"createdAt" example:"2026-09-27T10:00:00Z"`
	// 账号更新时间 (ISO 8601 UTC)
	UpdatedAt time.Time `json:"updatedAt" example:"2026-09-27T10:00:00Z"`
	// 账号状态 (active: 正常活跃, inactive: 未激活, banned: 已封禁)
	Status enum.Status `json:"status" example:"active" enums:"active,inactive,banned"`
}

// ProjectProfile 把用户行投影为档案视图；时间统一归一到 UTC。
func ProjectProfile(entity *sqlc.User) Profile {
	if entity == nil {
		return Profile{}
	}
	return Profile{
		ID:              entity.ID,
		Name:            entity.Name,
		Email:           entity.Email,
		EmailVerified:   entity.EmailVerified,
		AvatarPath:      entity.AvatarPath,
		Role:            entity.Role,
		Username:        entity.Username,
		DisplayUsername: entity.DisplayUsername,
		PhoneNumber:     entity.PhoneNumber,
		CreatedAt:       entity.CreatedAt.UTC(),
		UpdatedAt:       entity.UpdatedAt.UTC(),
		Status:          entity.Status,
	}
}

// Identity 把用户行投影为认证平台需要的身份值。
func Identity(u *sqlc.User) authentication.Identity {
	if u == nil {
		return authentication.Identity{}
	}
	return authentication.Identity{
		UserID: u.ID,
		Role:   string(u.Role),
		CustomClaims: map[string]any{
			"email_verified": u.EmailVerified,
		},
	}
}

// Notifiable 把用户行适配为通知接收者，路由 mail 与 sms 渠道。
func Notifiable(u *sqlc.User) notification.Notifiable {
	if u == nil {
		return nil
	}
	recipient := notification.NewRecipient(u.ID, "").WithEmail(u.Email)
	if u.PhoneNumber != nil && *u.PhoneNumber != "" {
		recipient.WithPhone(*u.PhoneNumber)
	}
	return recipient
}

// NormaliseEmail 统一邮箱大小写与首尾空白，与库中 idx_user_email 的口径一致。
func NormaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NormaliseUsername 统一用户名大小写与首尾空白，与库中 idx_user_username 的口径一致。
func NormaliseUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// IsBanned 判定账号是否处于封禁状态，已过期的封禁视为解除。
func IsBanned(u *sqlc.User, now time.Time) bool {
	if u == nil || !u.Banned {
		return false
	}
	return u.BanExpires == nil || u.BanExpires.After(now)
}

// GuardState 校验用户的通用封禁与活跃状态。
func GuardState(u *sqlc.User) error {
	if u == nil {
		return errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
	}
	if IsBanned(u, time.Now().UTC()) || u.Status == enum.StatusBanned {
		return errs.New(errs.CodeAuthAccountBanned, "The account has been banned")
	}
	if u.Status != enum.StatusActive {
		return errs.Forbidden("The account is not active")
	}
	return nil
}

// ClientPolicy 实现普通客户端用户的守卫校验策略。
type ClientPolicy struct {
	RequireEmailVerification bool
}

// ValidateUser 校验客户端账号活跃、未被封禁，且按配置校验邮箱验证状态。
func (p ClientPolicy) ValidateUser(_ context.Context, user *sqlc.User) error {
	if err := GuardState(user); err != nil {
		return err
	}
	if p.RequireEmailVerification && !user.EmailVerified {
		return errs.New(errs.CodeAuthEmailNotVerified, "The email address has not been verified")
	}
	return nil
}

// AdminPolicy 实现后台管理端管理员的守卫校验策略。
type AdminPolicy struct{}

// ValidateUser 校验管理员账号具备系统管理员角色且处于活跃可用状态。
func (p AdminPolicy) ValidateUser(ctx context.Context, user *sqlc.User) error {
	if user == nil {
		return errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
	}
	if id, ok := authentication.FromContext(ctx); ok {
		roles := id.AllRoles()
		if len(roles) > 0 && !slices.Contains(roles, enum.RoleAdmin.String()) {
			return ErrRoleRestricted
		}
	}
	if user.Role != enum.RoleAdmin {
		return ErrRoleRestricted
	}
	return GuardState(user)
}

var (
	_ authentication.Policy[*sqlc.User] = ClientPolicy{}
	_ authentication.Policy[*sqlc.User] = AdminPolicy{}
)

// FieldCannotBeNull 用于库层 NOT NULL、因而不能接受显式 null 的字段。
func FieldCannotBeNull(field string) error {
	return errs.ValidationKeys(map[string][]errs.MessageKey{
		field: {{Key: "not_null"}},
	})
}

// MapUniqueConflict 把唯一索引冲突翻译成具体的业务错误码。
func MapUniqueConflict(err error) error {
	constraint, ok := errs.UniqueConstraint(err)
	if !ok {
		return errs.Internal(err)
	}

	switch constraint {
	case indexUserEmail:
		return errs.New(errs.CodeAuthEmailAlreadyExists, "An account with this email address already exists")
	case indexUserUsername:
		return errs.New(errs.CodeAuthUsernameTaken, "The username has already been taken")
	case indexUserPhoneNumber:
		return errs.New(errs.CodeAuthPhoneNumberTaken, "The phone number has already been taken")
	default:
		return errs.Conflict("The user conflicts with an existing record")
	}
}

// EnsureUsernameAvailable 预判用户名是否已被他人占用。
func EnsureUsernameAvailable(ctx context.Context, db *database.DB, userID, username string) error {
	existing, err := db.Queries(ctx).GetUserByUsername(ctx, sqlc.GetUserByUsernameParams{Username: username})
	switch {
	case err == nil && existing.ID != userID:
		return errs.New(errs.CodeAuthUsernameTaken, "The username has already been taken")
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return errs.Internal(err)
	}
	return nil
}
