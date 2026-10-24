// 本文件覆盖驱动工厂：驱动单选、必填项，以及"启动就探测"这条约定。
package storage_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/storage"
)

// TestOpenLocalDisk 覆盖 local 驱动的正常装配路径。
func TestOpenLocalDisk(t *testing.T) {
	disk, err := storage.Open(t.Context(), config.StorageConfig{
		Driver: config.StorageDriverLocal,
		Local:  config.LocalStorageConfig{Root: t.TempDir()},
	})
	require.NoError(t, err)

	require.NoError(t, disk.Put(t.Context(), "a/b.txt", []byte("hi")))
	got, err := disk.Get(t.Context(), "a/b.txt")
	require.NoError(t, err)
	assert.Equal(t, []byte("hi"), got)
}

// TestOpenRejectsUnknownDriver 确认驱动是单选：写错不会被静默回退到本地，
// 也不会像旧实现那样把 "minio" / "oss" 之类的别名当成 s3。
func TestOpenRejectsUnknownDriver(t *testing.T) {
	for _, driver := range []string{"", "minio", "oss", "Local", "s3 "} {
		t.Run(driver, func(t *testing.T) {
			_, err := storage.Open(t.Context(), config.StorageConfig{
				Driver: driver,
				Local:  config.LocalStorageConfig{Root: t.TempDir()},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "storage driver")
		})
	}
}

// TestOpenS3RequiresConfig 确认选了 s3 却没给参数时在构造期失败。
func TestOpenS3RequiresConfig(t *testing.T) {
	_, err := storage.Open(t.Context(), config.StorageConfig{Driver: config.StorageDriverS3})
	require.Error(t, err)
}

// TestOpenProbesDisk 确认启动期的可用性探测确实在跑：只读根目录必须让装配失败，
// 而不是等到第一次上传才暴露。
func TestOpenProbesDisk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行时不具备权限语义，跳过只读目录用例")
	}

	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o500))
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	_, err := storage.Open(t.Context(), config.StorageConfig{
		Driver: config.StorageDriverLocal,
		Local:  config.LocalStorageConfig{Root: root},
	})
	require.Error(t, err, "不可写的根目录必须让装配失败")
}
