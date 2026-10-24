package storage

import (
	"fmt"
	"sync"

	"bgin/internal/config"
)

// DriverFactory 是根据配置构造底层 Disk 实例的工厂函数契约。
type DriverFactory func(cfg config.StorageConfig) (Disk, error)

var (
	driverMu sync.RWMutex
	drivers  = map[string]DriverFactory{
		config.StorageDriverLocal: func(cfg config.StorageConfig) (Disk, error) {
			return NewLocalDisk(cfg.Local.Root, cfg.Local.URL)
		},
		config.StorageDriverS3: func(cfg config.StorageConfig) (Disk, error) {
			return NewS3Disk(cfg.S3)
		},
	}
)

// RegisterDriver 注册新的存储驱动工厂，实现 OCP 开放-封闭架构扩展。
func RegisterDriver(name string, factory DriverFactory) {
	driverMu.Lock()
	defer driverMu.Unlock()
	drivers[name] = factory
}

// createDriver 根据配置动态实例化对应的存储驱动。
func createDriver(cfg config.StorageConfig) (Disk, error) {
	driverMu.RLock()
	factory, exists := drivers[cfg.Driver]
	driverMu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unknown storage driver %q, options: %s / %s",
			cfg.Driver, config.StorageDriverLocal, config.StorageDriverS3)
	}
	return factory(cfg)
}
