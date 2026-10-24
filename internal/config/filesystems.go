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
	Driver string             `mapstructure:"filesystem_disk"`
	Local  LocalStorageConfig `mapstructure:",squash"`
	S3     S3StorageConfig    `mapstructure:",squash"`
}

// LocalStorageConfig 描述本地文件系统存储驱动。
type LocalStorageConfig struct {
	Root string `mapstructure:"storage_local_root"`
	URL  string `mapstructure:"storage_local_url"`
}

// S3StorageConfig 描述 S3 兼容对象存储配置。
type S3StorageConfig struct {
	Endpoint        string `mapstructure:"aws_endpoint"`
	Bucket          string `mapstructure:"aws_bucket"`
	Region          string `mapstructure:"aws_default_region"`
	AccessKeyID     string `mapstructure:"aws_access_key_id"`
	SecretAccessKey string `mapstructure:"aws_secret_access_key"`
	UseSSL          bool   `mapstructure:"aws_use_ssl"`
	PublicURL       string `mapstructure:"aws_url"`
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
