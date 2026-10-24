package wechat

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"bgin/internal/config"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/errs"
)

// DefaultAppKey 是默认小程序应用的命名空间标识。
const DefaultAppKey = "default"

// Manager 管理全系统注册的多个微信小程序客户端实例（对齐 payment.Manager 架构）。
//
// 线程安全，支持多小程序多主体注册、按应用名或 AppID 快速检索以及默认应用回退。
type Manager struct {
	defaultApp string
	clients    map[string]MiniProgramClient
	byAppID    map[string]MiniProgramClient
	mu         sync.RWMutex
}

// NewManager 根据 WeChatConfig 构造多应用管理器。
// 若未配置任何小程序凭据，返回 nil, nil，调用方据此判断微信能力未启用。
func NewManager(cfg config.WeChatConfig, cacheClient cache.Store, opts ...Option) (*Manager, error) {
	if !cfg.Declared() {
		return nil, nil //nolint:nilnil // returning nil manager when unconfigured is intentional
	}

	apps, err := cfg.Applications()
	if err != nil {
		return nil, fmt.Errorf("wechat: failed to parse applications config: %w", err)
	}

	m := &Manager{
		clients: make(map[string]MiniProgramClient, len(apps)),
		byAppID: make(map[string]MiniProgramClient, len(apps)),
	}

	for name, appCfg := range apps {
		client := NewClient(appCfg, cacheClient, opts...)
		if client == nil {
			continue
		}

		m.clients[name] = client
		m.byAppID[appCfg.AppID] = client

		if m.defaultApp == "" || name == DefaultAppKey {
			m.defaultApp = name
		}
	}

	return m, nil
}

// Default 返回默认的小程序客户端实例。
func (m *Manager) Default() (MiniProgramClient, error) {
	if m == nil {
		return nil, errs.ServiceUnavailable("wechat manager is not configured")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	client, ok := m.clients[m.defaultApp]
	if !ok || client == nil {
		return nil, errs.ServiceUnavailable("wechat default miniprogram is not configured")
	}

	return client, nil
}

// App 根据配置中的应用标识名称（如 "user"、"merchant" 或 "default"）检索对应的小程序客户端。
func (m *Manager) App(name string) (MiniProgramClient, error) {
	if m == nil {
		return nil, errs.ServiceUnavailable("wechat manager is not configured")
	}

	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed == DefaultAppKey {
		return m.Default()
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	client, ok := m.clients[trimmed]
	if !ok || client == nil {
		return nil, errs.NotFound(fmt.Sprintf("wechat miniprogram app %q not found", trimmed))
	}

	return client, nil
}

// AppByID 根据微信分配的实际 AppID（如 "wx123456..."）检索对应的小程序客户端。
func (m *Manager) AppByID(appID string) (MiniProgramClient, error) {
	if m == nil {
		return nil, errs.ServiceUnavailable("wechat manager is not configured")
	}

	trimmed := strings.TrimSpace(appID)
	if trimmed == "" {
		return m.Default()
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	client, ok := m.byAppID[trimmed]
	if !ok || client == nil {
		return nil, errs.NotFound(fmt.Sprintf("wechat miniprogram appid %q not found", trimmed))
	}

	return client, nil
}

// Register 动态注册或覆盖一个小程序客户端实例（主要用于扩展、自定义适配器或测试 Mock）。
func (m *Manager) Register(name string, client MiniProgramClient) {
	if m == nil || client == nil {
		return
	}

	key := cmp.Or(strings.TrimSpace(name), DefaultAppKey)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.clients[key] = client
	if client.AppID() != "" {
		m.byAppID[client.AppID()] = client
	}

	if m.defaultApp == "" || key == DefaultAppKey {
		m.defaultApp = key
	}
}

// Applications 返回当前已注册的全部小程序应用名称列表（按字母升序排序）。
func (m *Manager) Applications() []string {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	return slices.Sorted(maps.Keys(m.clients))
}
