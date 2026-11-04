// Package authorization 封装基于 Casbin 的静态 RBAC 权限校验器（ADR-0003）。
//
// 模型与策略在编译期嵌入二进制：权限矩阵是代码契约的一部分，随版本发布演进。
package authorization

import (
	"context"
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"
	"github.com/samber/oops"

	"cgin/pkg/authentication"
	"cgin/pkg/errs"
)

const (
	// ResourceUsers 声明后台用户管理资源标识。
	ResourceUsers = "users"

	// ActionCreate 声明新建资源动作。
	ActionCreate = "create"
	// ActionList 声明分页列举资源动作。
	ActionList = "list"
	// ActionRead 声明读取单个资源详情动作。
	ActionRead = "read"
	// ActionUpdate 声明局部或全量更新资源动作。
	ActionUpdate = "update"
	// ActionDelete 声明删除资源动作。
	ActionDelete = "delete"
)

//go:embed rbac/model.conf
var embeddedModel string

//go:embed rbac/policy.csv
var embeddedPolicy string

// Enforcer 持有并发安全的 Casbin 实例，供路由级鉴权与业务用例共用。
type Enforcer struct {
	inner *casbin.SyncedEnforcer
}

// New 装载内嵌的 RBAC 模型与策略并返回并发安全的校验器。
func New() (*Enforcer, error) {
	m, err := model.NewModelFromString(embeddedModel)
	if err != nil {
		return nil, oops.In("authorization").Wrapf(err, "load casbin model")
	}

	e, err := casbin.NewSyncedEnforcer(m)
	if err != nil {
		return nil, oops.In("authorization").Wrapf(err, "init casbin enforcer")
	}

	if err := loadEmbeddedPolicy(e); err != nil {
		return nil, err
	}
	return &Enforcer{inner: e}, nil
}

// CanRole 判断给定角色是否被允许在指定资源上执行指定动作。
func (e *Enforcer) CanRole(role, resource, action string) (bool, error) {
	if role == "" {
		return false, nil
	}
	allowed, err := e.inner.Enforce(role, resource, action)
	if err != nil {
		return false, oops.In("authorization").
			With("role", role, "resource", resource, "action", action).
			Wrapf(err, "casbin enforce")
	}
	return allowed, nil
}

// CanRoles 判断给定的多个角色中是否有任意一个具备目标资源动作权限。
func (e *Enforcer) CanRoles(roles []string, resource, action string) bool {
	return slices.ContainsFunc(roles, func(role string) bool {
		allowed, err := e.CanRole(role, resource, action)
		return err == nil && allowed
	})
}

// Can 从上下文提取认证身份并校验是否具备指定资源动作权限。
func (e *Enforcer) Can(ctx context.Context, resource, action string) bool {
	id, ok := authentication.FromContext(ctx)
	if !ok {
		return false
	}
	roles := id.AllRoles()
	if len(roles) == 0 {
		return false
	}
	if len(roles) == 1 {
		allowed, err := e.CanRole(roles[0], resource, action)
		return err == nil && allowed
	}
	return e.CanRoles(roles, resource, action)
}

// Authorize 从上下文校验当前身份是否具备指定权限；未认证返回 401，无权限返回 403。
func (e *Enforcer) Authorize(ctx context.Context, resource, action string) error {
	id, err := authentication.RequireIdentity(ctx)
	if err != nil {
		return err
	}
	if !e.CanRoles(id.AllRoles(), resource, action) {
		return errs.Forbidden("You do not have permission to perform this action")
	}
	return nil
}

func loadEmbeddedPolicy(e *casbin.SyncedEnforcer) error {
	for idx, raw := range strings.Split(embeddedPolicy, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := persist.LoadPolicyLine(line, e.GetModel()); err != nil {
			return fmt.Errorf("authorization: parse policy line %d: %w", idx+1, err)
		}
	}
	return nil
}
