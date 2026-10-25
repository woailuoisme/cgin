package storage_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/internal/platform/storage"
)

// TestStorage_RoleInterfaces 验证 Disk 对原子角色契约的组合与满足。
func TestStorage_RoleInterfaces(t *testing.T) {
	disk, _ := newLocalDisk(t, "http://localhost:8080")

	// 编译期与运行时角色接口断言 (ISP)
	var _ storage.Reader = disk
	var _ storage.Writer = disk
	var _ storage.Deleter = disk
	var _ storage.Inspector = disk
	var _ storage.PublicURLer = disk
	var _ storage.Presigner = disk
	var _ storage.Pinger = disk

	assert.Equal(t, "local", disk.Driver())
}

// TestStorage_CapabilityDetection 验证预签名能力的显式探查机制 (LSP)。
func TestStorage_CapabilityDetection(t *testing.T) {
	local, _ := newLocalDisk(t, "http://localhost:8080")

	// 1. 本地磁盘不支持预签名
	assert.False(t, storage.SupportsPresign(local))
	presigner, ok := storage.AsPresigner(local)
	assert.False(t, ok)
	assert.Nil(t, presigner)

	// 2. nil 磁盘安全探查
	assert.False(t, storage.SupportsPresign(nil))
	_, ok = storage.AsPresigner(nil)
	assert.False(t, ok)
}

// TestStorage_DriverDispatch 验证内置驱动按名称分发，未知驱动立即报错。
func TestStorage_DriverDispatch(t *testing.T) {
	t.Run("本地驱动按名称分发", func(t *testing.T) {
		cfg := config.StorageConfig{
			Driver: config.StorageDriverLocal,
			Local:  config.LocalStorageConfig{Root: t.TempDir()},
		}

		disk, err := storage.Open(t.Context(), cfg)
		require.NoError(t, err)
		require.NotNil(t, disk)
	})

	t.Run("未知驱动返回错误", func(t *testing.T) {
		_, err := storage.Open(t.Context(), config.StorageConfig{Driver: "custom_mock"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown storage driver")
	})
}

// TestStorage_NullByteSecurity 验证路径解析对 Null-Byte (\x00) 注入的拦截防御。
func TestStorage_NullByteSecurity(t *testing.T) {
	disk, _ := newLocalDisk(t, "")

	ctx := t.Context()
	err := disk.Put(ctx, "test\x00malicious.txt", []byte("bad"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "The requested storage path is not allowed")

	_, err = disk.Get(ctx, "test\x00malicious.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "The requested storage path is not allowed")
}
