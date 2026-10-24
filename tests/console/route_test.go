package console_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/console"
)

func executeConsoleCommand(ctx context.Context, args ...string) (string, error) {
	cmd := console.NewRootCommand()
	stdoutBuf := new(bytes.Buffer)
	stderrBuf := new(bytes.Buffer)
	cmd.SetOut(stdoutBuf)
	cmd.SetErr(stderrBuf)
	cmd.SetArgs(args)

	err := cmd.ExecuteContext(ctx)
	return stdoutBuf.String(), err
}

// TestRouteListCommand_Basic 测试基础命令执行与表格渲染输出。
func TestRouteListCommand_Basic(t *testing.T) {
	out, err := executeConsoleCommand(context.Background(), "route:list")
	require.NoError(t, err)
	assert.Contains(t, out, "HTTP Route List")
	assert.Contains(t, out, "/livez")
	assert.Contains(t, out, "/readyz")
	assert.Contains(t, out, "system.Handler.Liveness")
	assert.Contains(t, out, "system.Handler.Index")
	assert.Contains(t, out, "Total:")
}

// TestRouteListCommand_DeprecatedRejected 测试废弃的空格层级命令被严格拒绝。
func TestRouteListCommand_DeprecatedRejected(t *testing.T) {
	_, err := executeConsoleCommand(context.Background(), "route", "list")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown command "route"`)
}

// TestRouteListCommand_FilterMethod 测试 HTTP 方法过滤。
func TestRouteListCommand_FilterMethod(t *testing.T) {
	out, err := executeConsoleCommand(context.Background(), "route:list", "--method", "post", "--json")
	require.NoError(t, err)

	var routes []console.RouteEntry
	err = json.Unmarshal([]byte(out), &routes)
	require.NoError(t, err)
	require.NotEmpty(t, routes)

	for _, r := range routes {
		assert.Equal(t, "POST", r.Method)
	}
}

// TestRouteListCommand_FilterPath 测试 URI 路径匹配过滤。
func TestRouteListCommand_FilterPath(t *testing.T) {
	out, err := executeConsoleCommand(context.Background(), "route:list", "--path", "/v1/auth", "--json")
	require.NoError(t, err)

	var routes []console.RouteEntry
	err = json.Unmarshal([]byte(out), &routes)
	require.NoError(t, err)
	require.NotEmpty(t, routes)

	for _, r := range routes {
		assert.Contains(t, r.Path, "/v1/auth")
	}
}

// TestRouteListCommand_Sorting 测试排序与逆序。
func TestRouteListCommand_Sorting(t *testing.T) {
	outAsc, err := executeConsoleCommand(context.Background(), "route:list", "--sort", "path", "--json")
	require.NoError(t, err)

	var routesAsc []console.RouteEntry
	err = json.Unmarshal([]byte(outAsc), &routesAsc)
	require.NoError(t, err)
	require.NotEmpty(t, routesAsc)

	outDesc, err := executeConsoleCommand(context.Background(), "route:list", "--sort", "path", "--reverse", "--json")
	require.NoError(t, err)

	var routesDesc []console.RouteEntry
	err = json.Unmarshal([]byte(outDesc), &routesDesc)
	require.NoError(t, err)
	require.Len(t, routesDesc, len(routesAsc))

	// 第一条和最后一条应相反
	assert.Equal(t, routesAsc[0].Path, routesDesc[len(routesDesc)-1].Path)
}

// TestRouteListCommand_InteractiveFallbackInNonTTY 测试非 TTY 环境下触发 -i 时的优雅降级。
func TestRouteListCommand_InteractiveFallbackInNonTTY(t *testing.T) {
	out, err := executeConsoleCommand(context.Background(), "route:list", "-i")
	require.NoError(t, err)
	assert.Contains(t, out, "Interactive mode requires a TTY terminal")
	assert.Contains(t, out, "HTTP Route List")
}
