package authentication

import (
	"cmp"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/samber/lo"
)

// ErrGuardNotFound 表示未找到请求的 Guard。
var ErrGuardNotFound = errors.New("authentication: guard not found")

// Manager 管理全系统注册的命名 StatefulGuard 实例，
// 支持多守卫（Multiple Guards）统一调度与按名称解析。
type Manager struct {
	mu           sync.RWMutex
	defaultGuard string
	guards       map[string]StatefulGuard
}

// NewManager 构造 Guard 注册中心。
func NewManager() *Manager {
	return &Manager{
		guards: make(map[string]StatefulGuard),
	}
}

// SetDefault 设置默认 Guard 名称。
func (m *Manager) SetDefault(name string) *Manager {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultGuard = strings.TrimSpace(name)
	return m
}

// Register 注册指定名称的已实例化 StatefulGuard。
func (m *Manager) Register(name string, guard StatefulGuard) *Manager {
	if guard == nil {
		return m
	}
	key := cmp.Or(strings.TrimSpace(name), "default")

	m.mu.Lock()
	defer m.mu.Unlock()
	m.guards[key] = guard
	if m.defaultGuard == "" {
		m.defaultGuard = key
	}
	return m
}

// Default 获取默认 Guard；未配置或未注册时返回错误。
func (m *Manager) Default() (StatefulGuard, error) {
	return m.Resolve()
}

// Guard 获取指定名称（或未指定时获取默认名称）的 Guard 实例。
func (m *Manager) Guard(name ...string) (StatefulGuard, error) {
	return m.Resolve(name...)
}

// Resolve 依据名称解析对应的 StatefulGuard 实例。未指定名称时回退至默认 Guard。
func (m *Manager) Resolve(name ...string) (StatefulGuard, error) {
	if m == nil {
		return nil, errors.New("authentication: manager is nil")
	}

	target := m.resolveTarget(name...)
	if target == "" {
		return nil, fmt.Errorf("%w: default guard not configured", ErrGuardNotFound)
	}

	m.mu.RLock()
	g, ok := m.guards[target]
	m.mu.RUnlock()
	if ok && g != nil {
		return g, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrGuardNotFound, target)
}

// MustGuard 获取指定名称的 Guard；未注册或失败时 panic。
func (m *Manager) MustGuard(name string) StatefulGuard {
	g, err := m.Resolve(name)
	if err != nil {
		panic("authentication: guard not registered or failed: " + name + ": " + err.Error())
	}
	return g
}

// resolveTarget 计算实际检索目标 Guard 的名称。
func (m *Manager) resolveTarget(name ...string) string {
	if target := strings.TrimSpace(lo.FirstOrEmpty(name)); target != "" {
		return target
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.defaultGuard
}

// GuardFor 是从 Manager 强类型提取具体泛型 *Guard[T] 的辅助函数。
// 如果目标 Guard 类型不符或不存在，返回类型明确的错误。
func GuardFor[T any](m *Manager, name ...string) (*Guard[T], error) {
	guard, err := m.Resolve(name...)
	if err != nil {
		return nil, err
	}
	g, ok := guard.(*Guard[T])
	if !ok {
		var zero T
		return nil, fmt.Errorf("authentication: guard %q is not of type *Guard[%T]", m.resolveTarget(name...), zero)
	}
	return g, nil
}

// GetGuard 是 GuardFor 的轻量提取别名，提取成功返回 (*Guard[T], true)，失败返回 (nil, false)。
func GetGuard[T any](m *Manager, name ...string) (*Guard[T], bool) {
	g, err := GuardFor[T](m, name...)
	return g, err == nil
}
