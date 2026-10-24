// Package realtime 提供基于 Centrifugo 的高可用实时通信基础设施驱动与协议定义。
package realtime

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/samber/lo"
	"github.com/samber/oops"

	"bgin/internal/platform/authentication"
)

// Authenticatable 表示实时频道鉴权可操作的用户实体契约。
type Authenticatable interface {
	GetAuthIdentifier() string
	GetGuardName() string
	GetRole() string
}

// GenericUser 泛用可认证用户结构，方便测试或直接构建。
type GenericUser struct {
	ID        string
	GuardName string
	Role      string
}

var _ Authenticatable = (*GenericUser)(nil)

// GetAuthIdentifier 返回用户主键标识。
func (u *GenericUser) GetAuthIdentifier() string {
	if u == nil {
		return ""
	}
	return u.ID
}

// GetGuardName 返回认证 Guard 名称。
func (u *GenericUser) GetGuardName() string {
	if u == nil {
		return ""
	}
	return u.GuardName
}

// GetRole 返回用户权限角色。
func (u *GenericUser) GetRole() string {
	if u == nil {
		return ""
	}
	return u.Role
}

// Private 构造标准的私有频道名称。
func Private(format string, args ...any) string {
	name := fmt.Sprintf(format, args...)
	if !strings.HasPrefix(name, "private:") && !strings.HasPrefix(name, "private-") {
		return "private:" + name
	}
	return name
}

// Presence 构造标准的成员状态频道名称。
func Presence(format string, args ...any) string {
	name := fmt.Sprintf(format, args...)
	if !strings.HasPrefix(name, "presence:") && !strings.HasPrefix(name, "presence-") {
		return "presence:" + name
	}
	return name
}

// Public 构造标准的公共广播频道名称。
func Public(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// AllowUser 注册标准用户私有频道规则（仅允许自身 ID 匹配 {id} 参数或拥有管理员角色的用户订阅）。
func AllowUser(r *Registry, pattern string) {
	if r == nil {
		return
	}
	r.Register(pattern, func(ctx context.Context, user User, channel string, params map[string]string) error {
		actor := resolveActor(ctx, user)
		if actor == nil || actor.GetAuthIdentifier() == "" {
			return oops.
				Code("UNAUTHORIZED").
				In("AllowUser."+pattern).
				With("channel", channel).
				Errorf("authentication required for user channel")
		}

		targetID := strings.TrimSpace(params["id"])
		if targetID != "" && actor.GetAuthIdentifier() == targetID {
			return nil
		}

		if actor.GetRole() == "admin" || actor.GetRole() == "super_admin" {
			return nil
		}

		return oops.
			Code("FORBIDDEN").
			In("AllowUser."+pattern).
			With("channel", channel).
			Errorf("permission denied for user channel")
	})
}

// AllowRole 注册限定指定角色访问的声明式频道策略。
func AllowRole(r *Registry, pattern string, allowedRoles ...string) {
	if r == nil || len(allowedRoles) == 0 {
		return
	}
	r.Register(pattern, func(ctx context.Context, user User, channel string, _ map[string]string) error {
		actor := resolveActor(ctx, user)
		if actor == nil || actor.GetAuthIdentifier() == "" {
			return oops.
				Code("UNAUTHORIZED").
				In("AllowRole."+pattern).
				With("channel", channel).
				Errorf("authentication required for role channel")
		}

		role := actor.GetRole()
		if slices.Contains(allowedRoles, role) {
			return nil
		}

		return oops.
			Code("FORBIDDEN").
			In("AllowRole."+pattern).
			With("channel", channel).
			Errorf("insufficient permissions for channel: required one of %v", allowedRoles)
	})
}

// AllowPublic 声明公开广播频道白名单，任何客户端均可自由订阅（满足 Fail-Closed 下的显式放行要求）。
func AllowPublic(r *Registry, patterns ...string) {
	if r == nil {
		return
	}
	r.AllowPublic(patterns...)
}

// AllowGuard 注册限定特定 Guard 访问的声明式频道策略。
func AllowGuard(r *Registry, pattern string, allowedGuards ...string) {
	if r == nil || len(allowedGuards) == 0 {
		return
	}
	r.Register(pattern, func(ctx context.Context, user User, channel string, _ map[string]string) error {
		guard := resolveGuard(ctx, user)
		if slices.Contains(allowedGuards, guard) {
			return nil
		}

		return oops.
			Code("FORBIDDEN").
			In("AllowGuard."+pattern).
			With("channel", channel).
			Errorf("guard required for channel: %v", allowedGuards)
	})
}

// Channel 注册基于目标 ID 谓词匹配的声明式频道策略。
func Channel(r *Registry, pattern string, policy func(ctx context.Context, actor Authenticatable, id string) bool) {
	if r == nil || policy == nil {
		return
	}
	r.Register(pattern, func(ctx context.Context, user User, channel string, params map[string]string) error {
		actor := resolveActor(ctx, user)
		targetID := params["id"]
		if !policy(ctx, actor, targetID) {
			return oops.
				Code("FORBIDDEN").
				In("channels."+pattern).
				With("channel", channel).
				Errorf("unauthorized channel subscription")
		}
		return nil
	})
}

// ChannelGuard 注册限定 Guard 名称访问的声明式频道策略。
func ChannelGuard(r *Registry, pattern, allowedGuard string) {
	AllowGuard(r, pattern, allowedGuard)
}

// ChannelPolicy 注册通用纯布尔谓词策略。
func ChannelPolicy(r *Registry, pattern string, policy func(ctx context.Context, actor Authenticatable) bool) {
	if r == nil || policy == nil {
		return
	}
	r.Register(pattern, func(ctx context.Context, user User, channel string, _ map[string]string) error {
		actor := resolveActor(ctx, user)
		if !policy(ctx, actor) {
			return oops.
				Code("FORBIDDEN").
				In("channels."+pattern).
				With("channel", channel).
				Errorf("unauthorized channel subscription")
		}
		return nil
	})
}

// ChannelPublish 注册客户端直接发布消息的声明式策略。
func ChannelPublish(r *Registry, pattern string, policy func(ctx context.Context, actor Authenticatable, channel string, data json.RawMessage) bool) {
	if r == nil || policy == nil {
		return
	}
	r.RegisterPublish(pattern, func(ctx context.Context, user User, channel string, data json.RawMessage, _ map[string]string) error {
		actor := resolveActor(ctx, user)
		if !policy(ctx, actor, channel, data) {
			return oops.
				Code("FORBIDDEN").
				In("publish."+pattern).
				With("channel", channel).
				Errorf("unauthorized client publish to channel")
		}
		return nil
	})
}

// RPC 快捷注册远程调用方法处理器。
func RPC(r *Registry, method string, handler RPCHandler) {
	if r == nil || handler == nil {
		return
	}
	r.RegisterRPC(method, handler)
}

func resolveActor(ctx context.Context, user User) Authenticatable {
	if id, ok := authentication.FromContext(ctx); ok {
		defaultGuard := lo.Ternary(id.Role == "admin" || id.Role == "super_admin", "admin", "client")
		return &GenericUser{
			ID:        id.UserID,
			Role:      cmp.Or(user.Role, id.Role),
			GuardName: cmp.Or(user.GuardName, defaultGuard),
		}
	}
	if user.ID != "" || user.GuardName != "" || user.Role != "" {
		return &GenericUser{
			ID:        user.ID,
			Role:      user.Role,
			GuardName: user.GuardName,
		}
	}
	return nil
}

func resolveGuard(ctx context.Context, user User) string {
	if actor := resolveActor(ctx, user); actor != nil {
		return actor.GetGuardName()
	}
	return user.GuardName
}
