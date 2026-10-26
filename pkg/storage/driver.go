package storage

import (
	"fmt"

	"cgin/internal/config"
)

// createDriver 根据配置实例化对应的存储驱动。
func createDriver(cfg config.StorageConfig) (Disk, error) {
	switch cfg.Driver {
	case config.StorageDriverLocal:
		return NewLocalDisk(cfg.Local.Root, cfg.Local.URL)
	case config.StorageDriverS3:
		return NewS3Disk(cfg.S3)
	default:
		return nil, fmt.Errorf("unknown storage driver %q, options: %s / %s",
			cfg.Driver, config.StorageDriverLocal, config.StorageDriverS3)
	}
}
