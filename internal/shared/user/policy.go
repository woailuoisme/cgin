package user

import (
	"context"
	"slices"
	"time"

	"bgin/internal/database/models"
	"bgin/internal/platform/authentication"
	"bgin/internal/platform/errs"
	"bgin/internal/shared/enums"
)

// ErrRoleRestricted 声明非目标角色访问受限错误（403 Forbidden）。
var ErrRoleRestricted = errs.Forbidden("Access restricted to administrators")

// GuardState 校验用户的通用封禁与活跃状态。
func GuardState(user *models.User) error {
	if user == nil {
		return errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
	}
	if user.IsBanned(time.Now().UTC()) || user.Status == models.StatusBanned {
		return errs.New(errs.CodeAuthAccountBanned, "The account has been banned")
	}
	if user.Status != models.StatusActive {
		return errs.Forbidden("The account is not active")
	}
	return nil
}

// ClientPolicy 实现普通客户端用户的守卫校验策略。
type ClientPolicy struct{}

// ValidateUser 校验客户端账号活跃且未被封禁。
func (p ClientPolicy) ValidateUser(_ context.Context, user *models.User) error {
	return GuardState(user)
}

// AdminPolicy 实现后台管理端管理员的守卫校验策略。
type AdminPolicy struct{}

// ValidateUser 校验管理员账号具备系统管理员角色且处于活跃可用状态。
func (p AdminPolicy) ValidateUser(ctx context.Context, user *models.User) error {
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
	_ authentication.Policy[*models.User] = ClientPolicy{}
	_ authentication.Policy[*models.User] = AdminPolicy{}
)
