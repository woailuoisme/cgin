// 本文件覆盖 S3 驱动中不需要真实服务端即可断言的部分：构造期校验、URL 组装与预签名。
//
// 预签名完全在本地计算签名、不产生网络往返，所以能离线断言"链接里确实有签名"，
// 而不只是"函数返回了字符串"。真正需要桶的操作（Put / Get / HealthCheck 成功路径）
// 必须对着真实对象存储跑，本文件不假装覆盖了它们。
package storage_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/storage"
)

// s3Config 返回一份最小可用的 S3 配置，指向本机共享栈里的 garage（明文）。
func s3Config() config.S3StorageConfig {
	return config.S3StorageConfig{
		Endpoint:        "127.0.0.1:3900",
		Bucket:          "bgin",
		Region:          "us-east-1",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
		UseSSL:          false,
	}
}

// TestNewS3DiskRequiresConfig 确认缺项在构造期就失败，且报错点名对应的环境变量。
func TestNewS3DiskRequiresConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.S3StorageConfig)
		want   string
	}{
		{"缺 endpoint", func(c *config.S3StorageConfig) { c.Endpoint = "" }, "AWS_ENDPOINT"},
		{"缺 bucket", func(c *config.S3StorageConfig) { c.Bucket = "" }, "AWS_BUCKET"},
		{"缺 access key", func(c *config.S3StorageConfig) { c.AccessKeyID = "" }, "AWS_ACCESS_KEY_ID"},
		{"缺 secret", func(c *config.S3StorageConfig) { c.SecretAccessKey = "" }, "AWS_SECRET_ACCESS_KEY"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := s3Config()
			tt.mutate(&cfg)

			_, err := storage.NewS3Disk(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestS3URL 覆盖公开地址的两种来源，并确认键里的相对片段被规整。
func TestS3URL(t *testing.T) {
	t.Run("未配置前缀时按 endpoint 与桶名推导", func(t *testing.T) {
		disk, err := storage.NewS3Disk(s3Config())
		require.NoError(t, err)

		address, err := disk.URL("users/1/a.png")
		require.NoError(t, err)
		assert.Equal(t, "http://127.0.0.1:3900/bgin/users/1/a.png", address)
	})

	t.Run("配置前缀后以它为准", func(t *testing.T) {
		cfg := s3Config()
		cfg.PublicURL = "https://cdn.example.com/"
		disk, err := storage.NewS3Disk(cfg)
		require.NoError(t, err)

		// 结尾斜杠与前导斜杠都不该拼出双斜杠。
		address, err := disk.URL("/users/2/b.png")
		require.NoError(t, err)
		assert.Equal(t, "https://cdn.example.com/users/2/b.png", address)

		// 键里的 ".." 会被消解，不会拼出回退路径。
		address, err = disk.URL("users/3/../4/c.png")
		require.NoError(t, err)
		assert.Equal(t, "https://cdn.example.com/users/4/c.png", address)
	})
}

// TestS3Presign 确认签名地址是真实签出来的：带签名参数与正确时效，且不合法 ttl 被拒绝。
func TestS3Presign(t *testing.T) {
	disk, err := storage.NewS3Disk(s3Config())
	require.NoError(t, err)
	ctx := t.Context()

	raw, err := disk.TemporaryURL(ctx, "users/1/avatar.png", 10*time.Minute)
	require.NoError(t, err)

	signed, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:3900", signed.Host)
	assert.Equal(t, "/bgin/users/1/avatar.png", signed.Path)
	assert.NotEmpty(t, signed.Query().Get("X-Amz-Signature"), "预签名必须真的带签名")
	assert.Equal(t, "600", signed.Query().Get("X-Amz-Expires"))

	raw, err = disk.PresignPut(ctx, "users/1/avatar.png", time.Minute)
	require.NoError(t, err)
	assert.Contains(t, raw, "X-Amz-Signature")

	// ttl 非正数会签出一个立即失效或永不过期的地址，必须直接拒绝。
	_, err = disk.TemporaryURL(ctx, "a.png", 0)
	require.Error(t, err)
	_, err = disk.PresignPut(ctx, "a.png", -time.Minute)
	require.Error(t, err)
}

// TestS3HealthCheckReportsFailure 确认探测失败时会带上桶名，便于定位是哪套存储出了问题。
func TestS3HealthCheckReportsFailure(t *testing.T) {
	cfg := s3Config()
	cfg.Endpoint = "127.0.0.1:1" // 必然拒绝连接的端口
	disk, err := storage.NewS3Disk(cfg)
	require.NoError(t, err)

	// minio 对连接错误有重试，用短超时把用例耗时压在 3 秒内；两种失败（拒绝连接或超时）
	// 都会被包装成带桶名的错误。
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	err = disk.HealthCheck(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "S3 bucket bgin")
}
