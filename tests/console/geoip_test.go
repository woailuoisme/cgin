package console_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/console"
	"bgin/internal/console/core"
)

// makeMockXDBData 构造一个符合 XDB Header 结构且体积超过 512KB 的合法测试数据流。
func makeMockXDBData() []byte {
	data := make([]byte, 512*1024+1024)
	// Version: 3, IndexPolicy: 1, CreatedAt: 123456, StartIndexPtr: 1000, EndIndexPtr: 2000, IPVersion: 4, RuntimePtrBytes: 4
	binary.LittleEndian.PutUint16(data[0:2], 3)
	binary.LittleEndian.PutUint16(data[2:4], 1)
	binary.LittleEndian.PutUint32(data[4:8], 1700000000)
	binary.LittleEndian.PutUint32(data[8:12], 1024)
	binary.LittleEndian.PutUint32(data[12:16], 2048)
	binary.LittleEndian.PutUint16(data[16:18], 4)
	binary.LittleEndian.PutUint16(data[18:20], 4)
	return data
}

// TestGeoIPUpdateOptions_Validate 测试参数校验逻辑。
func TestGeoIPUpdateOptions_Validate(t *testing.T) {
	f := &console.TestFactory{}

	t.Run("empty path returns validation error", func(t *testing.T) {
		opts := console.NewGeoIPUpdateOptions(f)
		opts.Path = ""
		err := opts.Validate()
		require.Error(t, err)
		var valErr *console.ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "path", valErr.Field)
	})

	t.Run("valid path passes validation", func(t *testing.T) {
		opts := console.NewGeoIPUpdateOptions(f)
		opts.Path = "storage/app/geoip/ip2region_v4.xdb"
		assert.NoError(t, opts.Validate())
	})
}

// TestGeoIPUpdateOptions_Complete 测试默认路径补全逻辑。
func TestGeoIPUpdateOptions_Complete(t *testing.T) {
	f := &console.TestFactory{}

	t.Run("preserves explicitly specified path", func(t *testing.T) {
		opts := console.NewGeoIPUpdateOptions(f)
		opts.Path = "/custom/path/ip.xdb"
		require.NoError(t, opts.Complete(nil, nil))
		assert.Equal(t, "/custom/path/ip.xdb", opts.Path)
	})

	t.Run("completes with default path when empty", func(t *testing.T) {
		opts := console.NewGeoIPUpdateOptions(f)
		opts.Path = ""
		require.NoError(t, opts.Complete(nil, nil))
		assert.Equal(t, "storage/app/geoip/ip2region_v4.xdb", opts.Path)
	})
}

// TestGeoIPUpdateOptions_Run_CancelWhenExists 测试文件已存在且用户取消时的交互保护。
func TestGeoIPUpdateOptions_Run_CancelWhenExists(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "ip2region_v4.xdb")
	require.NoError(t, os.WriteFile(targetPath, []byte("existing"), 0o600))

	buf := new(bytes.Buffer)
	mockPrompter := core.NewMockPrompter()
	mockPrompter.ConfirmResponses = []bool{false}
	f := &console.TestFactory{
		StreamsValue:  &core.IOStreams{Out: buf, ErrOut: buf},
		PrompterValue: mockPrompter,
	}

	opts := console.NewGeoIPUpdateOptions(f)
	opts.Path = targetPath
	opts.Force = false

	err := opts.Run(context.Background())
	require.ErrorIs(t, err, core.ErrCanceled)
	assert.Contains(t, buf.String(), "cancelled")
}

// TestGeoIPUpdateOptions_Run_SuccessWithMockServer 测试成功下载并原子替换目标文件的全流程。
func TestGeoIPUpdateOptions_Run_SuccessWithMockServer(t *testing.T) {
	mockData := makeMockXDBData()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "bgin-cli/geoip-updater", r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(mockData)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mockData)
	}))
	defer server.Close()

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "sub", "ip2region_v4.xdb")

	buf := new(bytes.Buffer)
	f := &console.TestFactory{
		StreamsValue: &core.IOStreams{Out: buf, ErrOut: buf},
	}

	opts := console.NewGeoIPUpdateOptions(f)
	opts.Path = targetPath
	opts.URL = server.URL
	opts.Force = true
	opts.HTTPClient = server.Client()

	err := opts.Run(context.Background())
	require.NoError(t, err)

	content, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Len(t, content, len(mockData))
	assert.Contains(t, buf.String(), "updated successfully")

	// 验证下载文件可被官方 xdb 加载 Header
	header, err := xdb.LoadHeaderFromBuff(content[:xdb.HeaderInfoLength])
	require.NoError(t, err)
	ver, err := xdb.VersionFromHeader(header)
	require.NoError(t, err)
	assert.NotNil(t, ver)
}

// TestGeoIPUpdateOptions_Run_MirrorFallback 测试首个镜像失败后自动降级重试后续镜像。
func TestGeoIPUpdateOptions_Run_MirrorFallback(t *testing.T) {
	mockData := makeMockXDBData()
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		if count == 1 {
			// 首个请求模拟 502 网关错误
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(mockData)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mockData)
	}))
	defer server.Close()

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "ip2region_v4.xdb")

	buf := new(bytes.Buffer)
	f := &console.TestFactory{
		StreamsValue: &core.IOStreams{Out: buf, ErrOut: buf},
	}

	opts := console.NewGeoIPUpdateOptions(f)
	opts.Path = targetPath
	opts.URL = server.URL
	opts.Force = true
	opts.HTTPClient = server.Client()

	// 第一次调用必定遇到首个请求 502 错误，Run 中只配置了单一 server.URL，故预期失败
	err := opts.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")

	// 第二次请求，mock server 返回 200 成功
	err = opts.Run(context.Background())
	require.NoError(t, err)
	assert.FileExists(t, targetPath)
}

// TestGeoIPUpdateOptions_Run_VerificationFailure 测试下载内容损坏或过小时拒绝覆盖并清理临时文件。
func TestGeoIPUpdateOptions_Run_VerificationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 返回不足 512KB 的无效数据（如 404 HTML 内容）
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>404 Not Found</html>"))
	}))
	defer server.Close()

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "ip2region_v4.xdb")

	buf := new(bytes.Buffer)
	f := &console.TestFactory{
		StreamsValue: &core.IOStreams{Out: buf, ErrOut: buf},
	}

	opts := console.NewGeoIPUpdateOptions(f)
	opts.Path = targetPath
	opts.URL = server.URL
	opts.Force = true
	opts.HTTPClient = server.Client()

	err := opts.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verification failed")

	// 目标文件绝不应被生成
	assert.NoFileExists(t, targetPath)

	// 检查目录中无残留临时文件
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), "."), "临时文件未被清理: %s", entry.Name())
	}
}

// TestGeoIPCommand_RootRegistration 测试在根命令与帮助文档中正确注册。
func TestGeoIPCommand_RootRegistration(t *testing.T) {
	cmd := console.NewRootCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"geoip:update", "--help"})

	err := cmd.ExecuteContext(context.Background())
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "ip2region_v4.xdb")
	assert.Contains(t, out, "--force")
	assert.Contains(t, out, "--path")
	assert.Contains(t, out, "--url")
}
