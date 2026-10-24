package search

import (
	"fmt"
	"strings"
	"sync"
)

// Driver 代表支持的搜索引擎驱动类型常量。
const (
	DriverMeilisearch = "meilisearch"
	DriverNoop        = "noop"
)

// DriverFactory 是构造搜索引擎客户端门面的工厂函数契约。
type DriverFactory func(cfg Config) (Client, error)

var (
	driverMu sync.RWMutex
	drivers  = map[string]DriverFactory{
		DriverMeilisearch: NewClientFromConfig,
		DriverNoop: func(_ Config) (Client, error) {
			return NewNoop(), nil
		},
		"": func(_ Config) (Client, error) {
			return NewNoop(), nil
		},
	}
)

// RegisterDriver 注册新的全文搜索驱动工厂，满足 OCP 开放-封闭架构扩展。
func RegisterDriver(name string, factory DriverFactory) {
	driverMu.Lock()
	defer driverMu.Unlock()
	drivers[strings.ToLower(strings.TrimSpace(name))] = factory
}

// createDriver 依据配置动态构造对应的搜索引擎客户端。
func createDriver(cfg Config) (Client, error) {
	driverName := strings.ToLower(strings.TrimSpace(cfg.Driver))
	if driverName == "" || driverName == DriverNoop {
		return NewNoop(), nil
	}

	driverMu.RLock()
	factory, exists := drivers[driverName]
	driverMu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unsupported search driver: %s", cfg.Driver)
	}

	return factory(cfg)
}
