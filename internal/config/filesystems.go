package config

import (
	"errors"
	"fmt"
	"strings"
)

// 文件存储驱动常量。
const (
	StorageDriverLocal = "local"
	StorageDriverS3    = "s3"
)

// StorageConfig 描述存储磁盘选择与参数（对齐 Laravel config/filesystems.php）。
type StorageConfig struct {
	Driver string `env:"FILESYSTEM_DISK" envDefault:"local"`
	Local  LocalStorageConfig
	S3     S3StorageConfig
}

// LocalStorageConfig 描述本地文件系统存储驱动。
type LocalStorageConfig struct {
	Root string `env:"STORAGE_LOCAL_ROOT" envDefault:"./storage/app/uploads"`
	URL  string `env:"STORAGE_LOCAL_URL"`
}

// S3StorageConfig 描述 S3 兼容对象存储配置。
type S3StorageConfig struct {
	Endpoint        string `env:"AWS_ENDPOINT"`
	Bucket          string `env:"AWS_BUCKET"`
	Region          string `env:"AWS_DEFAULT_REGION" envDefault:"us-east-1"`
	AccessKeyID     string `env:"AWS_ACCESS_KEY_ID"`
	SecretAccessKey string `env:"AWS_SECRET_ACCESS_KEY"`
	UseSSL          bool   `env:"AWS_USE_SSL" envDefault:"true"`
	PublicURL       string `env:"AWS_URL"`
}

// validateStorage 检查存储驱动合法性与必填字段。
func (c *Config) validateStorage() error {
	switch c.Storage.Driver {
	case StorageDriverLocal:
		if strings.TrimSpace(c.Storage.Local.Root) == "" {
			return errors.New("config: STORAGE_LOCAL_ROOT cannot be empty when FILESYSTEM_DISK=local")
		}
	case StorageDriverS3:
		s3 := c.Storage.S3
		for _, item := range []struct{ name, value string }{
			{"AWS_ENDPOINT", s3.Endpoint},
			{"AWS_BUCKET", s3.Bucket},
			{"AWS_ACCESS_KEY_ID", s3.AccessKeyID},
			{"AWS_SECRET_ACCESS_KEY", s3.SecretAccessKey},
		} {
			if strings.TrimSpace(item.value) == "" {
				return fmt.Errorf("config: %s is required when FILESYSTEM_DISK=s3", item.name)
			}
		}
	default:
		return fmt.Errorf("config: FILESYSTEM_DISK=%q is not a valid value, options: %s / %s",
			c.Storage.Driver, StorageDriverLocal, StorageDriverS3)
	}
	return nil
}
