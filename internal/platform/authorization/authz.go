// Package authorization 提供基于 Casbin 与内存只读快查的高性能 RBAC 权限授权与 Gate 门禁能力。
package authorization

import (
	"bytes"
	"context"
	"embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	stringadapter "github.com/casbin/casbin/v3/persist/string-adapter"

	"bgin/internal/platform/authentication"
	"bgin/internal/platform/errs"
)

// 常用资源与动作常量。
const (
	ResourceUsers = "users"

	ActionCreate = "create"
	ActionList   = "list"
	ActionRead   = "read"
	ActionUpdate = "update"
	ActionDelete = "delete"
)

// Resource 辅助函数，便捷返回资源名称字符串。
func Resource(name string) string {
	return name
}

//go:embed rbac/model.conf rbac/policy.csv
var rbacFS embed.FS

// Enforcer 判断角色对资源的操作权限，同时完整实现 authentication.Gate 契约。
type Enforcer struct {
	enforcer   *casbin.Enforcer
	fastLookup map[string]map[string]struct{} // role -> "resource:action" 预编译快速只读索引，纳秒级 O(1) 命中
}

var _ authentication.Gate = (*Enforcer)(nil)

// New 从内嵌策略文件构造 Enforcer 并预编译静态快查矩阵。
func New() (*Enforcer, error) {
	modelText, err := fs.ReadFile(rbacFS, "rbac/model.conf")
	if err != nil {
		return nil, fmt.Errorf("authorization: read model failed: %w", err)
	}
	policyText, err := fs.ReadFile(rbacFS, "rbac/policy.csv")
	if err != nil {
		return nil, fmt.Errorf("authorization: read policy failed: %w", err)
	}

	parsedModel, err := model.NewModelFromString(string(modelText))
	if err != nil {
		return nil, fmt.Errorf("authorization: parse model failed: %w", err)
	}

	enforcer, err := casbin.NewEnforcer(parsedModel, stringadapter.NewAdapter(string(policyText)))
	if err != nil {
		return nil, fmt.Errorf("authorization: new enforcer failed: %w", err)
	}
	// 策略全部来自内嵌文件，运行期没有任何变更来源。
	enforcer.EnableAutoSave(false)

	// 预编译静态策略表为只读 map，绝大多数权限校验以 O(1) 纳秒级完成
	fastLookup := buildFastLookup(policyText)

	return &Enforcer{
		enforcer:   enforcer,
		fastLookup: fastLookup,
	}, nil
}

// buildFastLookup 解析 policy.csv 规则集，构建 role -> "resource:action" 索引
func buildFastLookup(data []byte) map[string]map[string]struct{} {
	lookup := make(map[string]map[string]struct{})
	reader := csv.NewReader(bytes.NewReader(data))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1

	for {
		record, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}
		// 期待格式: p, role, resource, action
		if len(record) < 4 || strings.TrimSpace(record[0]) != "p" {
			continue
		}
		role := strings.TrimSpace(record[1])
		resource := strings.TrimSpace(record[2])
		action := strings.TrimSpace(record[3])
		if role == "" || resource == "" || action == "" {
			continue
		}

		if lookup[role] == nil {
			lookup[role] = make(map[string]struct{})
		}
		lookup[role][resource+":"+action] = struct{}{}
	}
	return lookup
}

// CanRole 判定指定单一角色是否被授予指定资源和动作的操作权限。
func (e *Enforcer) CanRole(role, resource, action string) (bool, error) {
	if role == "" || resource == "" || action == "" {
		return false, nil
	}

	// 1. O(1) 预编译静态只读快查命中
	if actions, ok := e.fastLookup[role]; ok {
		if _, allowed := actions[resource+":"+action]; allowed {
			return true, nil
		}
	}

	// 2. 回退至 Casbin 引擎兜底验证
	allowed, err := e.enforcer.Enforce(role, resource, action)
	if err != nil {
		return false, fmt.Errorf("authorization: enforce failed: %w", err)
	}
	return allowed, nil
}

// CanRoles 判定一组角色中是否有任一角色具备指定权限。
func (e *Enforcer) CanRoles(roles []string, resource, action string) bool {
	return slices.ContainsFunc(roles, func(role string) bool {
		allowed, _ := e.CanRole(role, resource, action)
		return allowed
	})
}

// Can 实现 authentication.Gate 契约：从上下文提取主体角色并判断权限。
func (e *Enforcer) Can(ctx context.Context, resource, action string) bool {
	id, ok := authentication.FromContext(ctx)
	if !ok || id.UserID == "" {
		return false
	}
	return e.CanRoles(id.AllRoles(), resource, action)
}

// Authorize 实现 authentication.Gate 契约：校验权限，无权限直接返回 403 Forbidden 统一错误。
func (e *Enforcer) Authorize(ctx context.Context, resource, action string) error {
	if !e.Can(ctx, resource, action) {
		return errs.Forbidden("You are not allowed to perform this action")
	}
	return nil
}

// Allows 实现 authentication.Gate 契约，是 Can 的语义别名。
func (e *Enforcer) Allows(ctx context.Context, resource, action string) bool {
	return e.Can(ctx, resource, action)
}

// Denies 实现 authentication.Gate 契约：判定当前请求主体是否被禁止该操作。
func (e *Enforcer) Denies(ctx context.Context, resource, action string) bool {
	return !e.Can(ctx, resource, action)
}
