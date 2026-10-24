//go:build integration

// 本文件对着真实对象存储跑通 S3 驱动的成功路径。
//
// 单元测试只能覆盖构造校验与预签名（签名在本地计算），真正的读写必须有一个可达的桶。
// 未配置 AWS_ENDPOINT 时跳过，因此不会给"只跑单元测试"的环境添麻烦；
// 配置齐了（例如本机共享栈里的 garage）就跑真东西。
package integration_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/storage"
)

// newS3Disk 读取当前环境的 S3 配置并构造驱动；没配 endpoint 就跳过。
func newS3Disk(t *testing.T) storage.Disk {
	t.Helper()

	cfg, err := config.Load()
	require.NoError(t, err, "加载配置失败")
	if cfg.Storage.S3.Endpoint == "" {
		t.Skip("未配置 AWS_ENDPOINT，跳过对象存储连测")
	}

	disk, err := storage.NewS3Disk(cfg.Storage.S3)
	require.NoError(t, err, "S3 配置不完整（endpoint / bucket / 密钥）")
	return disk
}

// fetch 发起一次 GET，回传状态码与正文。
func fetch(ctx context.Context, t *testing.T, rawURL string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}

// TestS3RoundTrip 覆盖读写往返与删除，并确认签名链接能被外部客户端真正取用。
func TestS3RoundTrip(t *testing.T) {
	disk := newS3Disk(t)
	ctx := t.Context()

	// 用时间戳隔离，避免与其他用例或人工调试踩到同一个键。
	key := fmt.Sprintf("tests/%d/hello.txt", time.Now().UnixNano())
	content := []byte("bgin storage integration")

	t.Cleanup(func() { _ = disk.Delete(t.Context(), key) })

	require.NoError(t, disk.Put(ctx, key, content))

	got, err := disk.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, content, got)

	exists, err := disk.Exists(ctx, key)
	require.NoError(t, err)
	assert.True(t, exists)

	size, err := disk.Size(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), size)

	// 预签名链接必须真的能下载到内容——这是"签名算对了"与"只是返回了一个字符串"的分界。
	signed, err := disk.TemporaryURL(ctx, key, 2*time.Minute)
	require.NoError(t, err)

	status, body := fetch(ctx, t, signed)
	require.Equal(t, http.StatusOK, status, "预签名下载应当成功")
	assert.Equal(t, content, body)

	// 篡改签名必须被拒绝，否则"签名"只是装饰。
	status, _ = fetch(ctx, t, signed+"0")
	assert.Equal(t, http.StatusForbidden, status, "签名字节被改动后必须拒绝")

	require.NoError(t, disk.Delete(ctx, key))

	exists, err = disk.Exists(ctx, key)
	require.NoError(t, err)
	assert.False(t, exists)

	// 删除不存在的对象是幂等的。
	assert.NoError(t, disk.Delete(ctx, key))
}

// TestS3HealthCheck 确认启动期探测能通过。
func TestS3HealthCheck(t *testing.T) {
	disk := newS3Disk(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	assert.NoError(t, disk.HealthCheck(ctx))
}
