package console_test

import (
	"context"
	"testing"

	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/console"
	"bgin/internal/console/core"
)

// TestRootCommand_Registration 验证所有预期的 CLI 命令均已按纯冒号方式注册，并正确分配到对应的命名空间分组。
func TestRootCommand_Registration(t *testing.T) {
	cmd := console.NewRootCommand()
	require.NotNil(t, cmd)

	// 1. 验证全部一等公民命令（纯冒号命令体系 + serve）
	expectedCommands := []string{
		"serve",
		"db:migrate",
		"db:seed",
		"db:fresh",
		"geoip:update",
		"jwt:secret",
		"make:crud",
		"queue:work",
		"queue:failed",
		"queue:retry",
		"queue:flush",
		"route:list",
		"scout:import",
		"scout:flush",
		"sentry:test",
		"temporal:work",
	}

	commandNames := lo.Map(cmd.Commands(), func(c *cobra.Command, _ int) string {
		return c.Name()
	})

	for _, expected := range expectedCommands {
		assert.Contains(t, commandNames, expected, "Root command should contain colon-only command: "+expected)
	}

	// 2. 验证空格层级父命令及已废除的 make:destroy 不再作为命令存在
	deprecatedTopCommands := []string{
		"queue",
		"route",
		"db",
		"geoip",
		"scout",
		"jwt",
		"make",
		"make:destroy",
	}
	for _, deprecated := range deprecatedTopCommands {
		assert.NotContains(t, commandNames, deprecated, "Deprecated command should not exist: "+deprecated)
	}

	// 3. 验证命名空间分组注册完整性与 GroupID 正确映射
	groupIDs := lo.Map(cmd.Groups(), func(g *cobra.Group, _ int) string {
		return g.ID
	})
	assert.ElementsMatch(t, []string{"db", "geoip", "jwt", "make", "queue", "route", "scout", "sentry", "temporal"}, groupIDs)

	cmdMap := lo.KeyBy(cmd.Commands(), func(c *cobra.Command) string {
		return c.Name()
	})

	assert.Empty(t, cmdMap["serve"].GroupID, "serve should have no group (root Available commands)")
	assert.Equal(t, "db", cmdMap["db:migrate"].GroupID)
	assert.Equal(t, "db", cmdMap["db:seed"].GroupID)
	assert.Equal(t, "db", cmdMap["db:fresh"].GroupID)
	assert.Equal(t, "geoip", cmdMap["geoip:update"].GroupID)
	assert.Equal(t, "jwt", cmdMap["jwt:secret"].GroupID)
	assert.Equal(t, "make", cmdMap["make:crud"].GroupID)
	assert.Equal(t, "queue", cmdMap["queue:work"].GroupID)
	assert.Equal(t, "queue", cmdMap["queue:failed"].GroupID)
	assert.Equal(t, "queue", cmdMap["queue:retry"].GroupID)
	assert.Equal(t, "queue", cmdMap["queue:flush"].GroupID)
	assert.Equal(t, "route", cmdMap["route:list"].GroupID)
	assert.Equal(t, "scout", cmdMap["scout:import"].GroupID)
	assert.Equal(t, "scout", cmdMap["scout:flush"].GroupID)
	assert.Equal(t, "sentry", cmdMap["sentry:test"].GroupID)
	assert.Equal(t, "temporal", cmdMap["temporal:work"].GroupID)
}

// TestParseQueueWeights 验证队列权重参数解析。
func TestParseQueueWeights(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]int
	}{
		{
			name:     "empty input defaults to default:1",
			input:    "",
			expected: map[string]int{"default": 1},
		},
		{
			name:     "single queue name without weight",
			input:    "critical",
			expected: map[string]int{"critical": 1},
		},
		{
			name:     "single queue with weight",
			input:    "critical:5",
			expected: map[string]int{"critical": 5},
		},
		{
			name:     "multiple queues with weights and whitespace",
			input:    "high:3, default:1, low:1 ",
			expected: map[string]int{"high": 3, "default": 1, "low": 1},
		},
		{
			name:     "invalid weight falls back to 1",
			input:    "custom:invalid",
			expected: map[string]int{"custom": 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := console.ParseQueueWeights(tc.input)
			assert.Equal(t, tc.expected, res)
		})
	}
}

// TestCommand_ValidationErrors 验证命令在缺少必填入参时的防御性报错（双模调用验证：冒号别名与层级子命令）。
func TestCommand_ValidationErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("queue:retry without arg", func(t *testing.T) {
		cmd := console.NewRootCommand()
		cmd.SetArgs([]string{"queue:retry"})
		err := cmd.ExecuteContext(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "please provide a job ID or 'all'")
	})

	t.Run("scout:import without model or --all", func(t *testing.T) {
		cmd := console.NewRootCommand()
		cmd.SetArgs([]string{"scout:import"})
		err := cmd.ExecuteContext(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "please specify a model name or use --all")
	})

	t.Run("scout:flush without model", func(t *testing.T) {
		cmd := console.NewRootCommand()
		cmd.SetArgs([]string{"scout:flush"})
		err := cmd.ExecuteContext(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "please provide a model name to flush")
	})

	t.Run("deprecated space-separated invocation is rejected", func(t *testing.T) {
		cmd := console.NewRootCommand()
		cmd.SetArgs([]string{"queue", "retry"})
		err := cmd.ExecuteContext(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown command "queue"`)
	})
}

// TestErrCanceled_Export 验证 console 包导出的 ErrCanceled 变量与 core.ErrCanceled 完全一致。
func TestErrCanceled_Export(t *testing.T) {
	require.ErrorIs(t, console.ErrCanceled, core.ErrCanceled)
	assert.Equal(t, "operation canceled by user", console.ErrCanceled.Error())
}
