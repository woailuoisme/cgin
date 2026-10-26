package user

import (
	"context"
	"slices"

	"cgin/internal/database/sqlc"
	"cgin/pkg/authentication"
	"cgin/pkg/errs"
	"cgin/shared/enums"
)

// ErrRoleRestricted 声明非目标角色访问受限错误（403 Forbidden）。
var ErrRoleRestricted = errs.Forbidden("Access restricted to administrators")

// ClientPolicy 实现普通客户端用户的守卫校验策略。
type ClientPolicy struct {
	// RequireEmailVerification 为 true 时拒绝尚未完成邮箱验证的账号。
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
		if len(roles) > 0 && !slices.Contains(roles, enums.RoleAdmin.String()) {
			return ErrRoleRestricted
		}
	}
	if user.Role != enums.RoleAdmin {
		return ErrRoleRestricted
	}
	return GuardState(user)
}

var (
	_ authentication.Policy[*sqlc.User] = ClientPolicy{}
	_ authentication.Policy[*sqlc.User] = AdminPolicy{}
)
