package authentication

import (
	"context"
	"sync"
	"time"

	"bgin/internal/platform/errs"
)

// AlwaysAllowGate 是无条件放行所有权限请求的测试桩。
type AlwaysAllowGate struct{}

// Can 无条件放行。
func (AlwaysAllowGate) Can(context.Context, string, string) bool { return true }

// Authorize 无条件放行。
func (AlwaysAllowGate) Authorize(context.Context, string, string) error { return nil }

// Allows 无条件放行。
func (AlwaysAllowGate) Allows(context.Context, string, string) bool { return true }

// Denies 无条件不拒绝。
func (AlwaysAllowGate) Denies(context.Context, string, string) bool { return false }

var (
	_ Gate = AlwaysAllowGate{}
	_ Gate = AlwaysDenyGate{}
	_ Gate = (*FakeGate)(nil)
)

// AlwaysDenyGate 是无条件拒绝所有权限请求的测试桩。
type AlwaysDenyGate struct{}

// Can 无条件拒绝。
func (AlwaysDenyGate) Can(context.Context, string, string) bool { return false }

// Authorize 无条件返回 403 拒绝错误。
func (AlwaysDenyGate) Authorize(context.Context, string, string) error {
	return errs.Forbidden("You are not allowed to perform this action")
}

// Allows 无条件拒绝。
func (AlwaysDenyGate) Allows(context.Context, string, string) bool { return false }

// Denies 无条件拒绝。
func (AlwaysDenyGate) Denies(context.Context, string, string) bool { return true }

// FakeGate 提供可细粒度预设权限的测试用门禁桩。
type FakeGate struct {
	mu           sync.RWMutex
	rules        map[string]bool
	defaultAllow bool
}

// NewFakeGate 构造具有默认策略的 FakeGate。
func NewFakeGate(defaultAllow bool) *FakeGate {
	return &FakeGate{
		rules:        make(map[string]bool),
		defaultAllow: defaultAllow,
	}
}

// Allow 显式放行指定资源与动作。
func (f *FakeGate) Allow(resource, action string) *FakeGate {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules[resource+":"+action] = true
	return f
}

// Deny 显式拒绝指定资源与动作。
func (f *FakeGate) Deny(resource, action string) *FakeGate {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules[resource+":"+action] = false
	return f
}

// Can 判定是否具有权限。
func (f *FakeGate) Can(_ context.Context, resource, action string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if allowed, ok := f.rules[resource+":"+action]; ok {
		return allowed
	}
	return f.defaultAllow
}

// Authorize 鉴权，无权限时返回 403 错误。
func (f *FakeGate) Authorize(ctx context.Context, resource, action string) error {
	if !f.Can(ctx, resource, action) {
		return errs.Forbidden("You are not allowed to perform this action")
	}
	return nil
}

// Allows 是 Can 的语义别名。
func (f *FakeGate) Allows(ctx context.Context, resource, action string) bool {
	return f.Can(ctx, resource, action)
}

// Denies 判定是否被拒绝。
func (f *FakeGate) Denies(ctx context.Context, resource, action string) bool {
	return !f.Can(ctx, resource, action)
}

// FakeGuard 提供实现 StatefulGuard 契约的测试桩。
type FakeGuard struct {
	mu          sync.RWMutex
	tokenValid  bool
	checkResult bool
	claims      *Claims
	identity    Identity
	err         error
}

var _ StatefulGuard = (*FakeGuard)(nil)

// NewFakeGuard 构造 FakeGuard。
func NewFakeGuard(authenticated bool) *FakeGuard {
	fg := &FakeGuard{
		tokenValid:  authenticated,
		checkResult: authenticated,
	}
	if authenticated {
		fg.identity = Identity{
			UserID:         "test-user-id",
			Role:           "admin",
			Roles:          []string{"admin"},
			TokenID:        "test-token-id",
			TokenExpiresAt: time.Now().Add(time.Hour),
		}
		fg.claims = &Claims{
			Role: "admin",
			Type: TypeAccess,
		}
		fg.claims.Subject = "test-user-id"
		fg.claims.ID = "test-token-id"
	}
	return fg
}

// SetIdentity 设置模拟的身份信息。
func (f *FakeGuard) SetIdentity(id Identity) *FakeGuard {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identity = id
	f.checkResult = id.UserID != ""
	return f
}

// SetError 设置预设错误。
func (f *FakeGuard) SetError(err error) *FakeGuard {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
	return f
}

// AuthenticateToken 模拟令牌认证。
func (f *FakeGuard) AuthenticateToken(ctx context.Context, _ string) (context.Context, *Claims, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.err != nil {
		return ctx, nil, f.err
	}
	if !f.tokenValid {
		return ctx, nil, errs.Unauthorized(errs.CodeAuthTokenInvalid, "Invalid token")
	}
	newCtx := With(ctx, f.identity)
	return newCtx, f.claims, nil
}

// Check 模拟检查认证状态。
func (f *FakeGuard) Check(ctx context.Context) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	id, ok := FromContext(ctx)
	if ok && id.UserID != "" {
		return true
	}
	return f.checkResult
}

// InvalidateToken 模拟注销。
func (f *FakeGuard) InvalidateToken(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenValid = false
	f.checkResult = false
	return nil
}
