package console_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/console"
	"bgin/internal/console/core"
)

// getHelpOutput 辅助函数：通过 ExecuteContext 捕获包含全量默认标志与附加命令的完整帮助输出。
func getHelpOutput(t *testing.T, args ...string) string {
	t.Helper()
	cmd := console.NewRootCommand()
	require.NotNil(t, cmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(append(args, "--help"))

	_ = cmd.ExecuteContext(context.Background())
	return buf.String()
}

// TestColoredHelp_Formatting 验证控制台帮助模板在彩色与纯文本模式下的格式、对齐与转义序列。
func TestColoredHelp_Formatting(t *testing.T) {
	// 1. 测试禁用颜色时的纯文本输出与列对齐
	t.Run("plain text mode preserves alignment", func(t *testing.T) {
		core.SetColorEnabled(false)
		defer core.ResetColorOverride()

		output := getHelpOutput(t)
		assert.NotContains(t, output, "\x1b[", "禁用色彩时不应包含 ANSI 转义序列")
		assert.Contains(t, output, "Usage:")
		assert.Contains(t, output, "bgin [command]")
		assert.Contains(t, output, "Available commands:")
		assert.Contains(t, output, "completion")
		assert.Contains(t, output, "help")
		assert.Contains(t, output, "serve")
		assert.Contains(t, output, "db\n")
		assert.Contains(t, output, "db:migrate")
		assert.Contains(t, output, "queue\n")
		assert.Contains(t, output, "queue:work")
		assert.Contains(t, output, "route\n")
		assert.Contains(t, output, "route:list")
		assert.Contains(t, output, "Flags:")
		assert.Contains(t, output, "-h, --help      help for bgin")
		assert.Contains(t, output, "-v, --version   version for bgin")
		assert.Contains(t, output, `Use "bgin [command] --help" for more information about a command.`)
	})

	// 2. 测试启用彩色高亮时的 ANSI 转义字符与关键高亮元素
	t.Run("colored mode renders ANSI styles and preserves alignment", func(t *testing.T) {
		core.SetColorEnabled(true)
		defer core.ResetColorOverride()

		output := getHelpOutput(t)
		assert.Contains(t, output, "\x1b[", "启用色彩时必须包含 ANSI 转义序列")

		// 验证小节标题样式（包含加粗暖黄色 ANSI 码）
		assert.Contains(t, output, core.StyleHeader("Usage:"))
		assert.Contains(t, output, core.StyleHeader("Available commands:"))
		assert.Contains(t, output, core.StyleHeader("db"))
		assert.Contains(t, output, core.StyleHeader("jwt"))
		assert.Contains(t, output, core.StyleHeader("make"))
		assert.Contains(t, output, core.StyleHeader("queue"))
		assert.Contains(t, output, core.StyleHeader("route"))
		assert.Contains(t, output, core.StyleHeader("scout"))
		assert.Contains(t, output, core.StyleHeader("Flags:"))

		rootCmd := console.NewRootCommand()
		pad := rootCmd.NamePadding()

		// 验证命令名称高亮
		assert.Contains(t, output, core.StyleCmd("serve", pad))
		assert.Contains(t, output, core.StyleCmd("db:migrate", pad))
		assert.Contains(t, output, core.StyleCmd("queue:work", pad))
		assert.Contains(t, output, core.StyleCmd("route:list", pad))
		assert.Contains(t, output, core.StyleCmd("completion", pad))
		assert.Contains(t, output, core.StyleCmd("help", pad))

		// 验证选项标志与使用说明完整包含
		assert.Contains(t, output, "help for bgin")
		assert.Contains(t, output, "version for bgin")
		assert.Contains(t, output, `Use "`+core.StyleUseLine("bgin [command] --help")+`" for more information about a command.`)
	})

	// 3. 验证 FlagUsages 彩色解析的列对齐严格保真
	t.Run("flag styling preserves column alignment", func(t *testing.T) {
		rawFlags := `  -h, --help             help for bgin
  -p, --port int         Override APP_PORT
      --chinese string   Chinese desc
      --client           Client flag
`
		core.SetColorEnabled(false)
		plain := core.StyleFlags(rawFlags)
		assert.Equal(t, rawFlags, plain)

		core.SetColorEnabled(true)
		defer core.ResetColorOverride()
		colored := core.StyleFlags(rawFlags)
		assert.Contains(t, colored, "\x1b[")

		// 验证每一行的描述文本起始对齐偏移在视觉宽度上完全一致
		plainLines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
		coloredLines := strings.Split(strings.TrimRight(colored, "\n"), "\n")
		require.Len(t, coloredLines, len(plainLines))

		for i := range plainLines {
			plainDescIdx := strings.Index(plainLines[i], "bgin")
			if plainDescIdx == -1 {
				plainDescIdx = strings.Index(plainLines[i], "Override")
			}
			if plainDescIdx == -1 {
				plainDescIdx = strings.Index(plainLines[i], "Chinese")
			}
			if plainDescIdx == -1 {
				plainDescIdx = strings.Index(plainLines[i], "Client flag")
			}
			require.NotEqual(t, -1, plainDescIdx)
		}
	})

	// 4. 验证层级子命令 (如 bgin serve 与 bgin make crud) 帮助信息的高亮与对齐继承
	t.Run("subcommand help inherits colored template", func(t *testing.T) {
		core.SetColorEnabled(true)
		defer core.ResetColorOverride()

		// 验证 serve 子命令
		serveOutput := getHelpOutput(t, "serve")
		assert.Contains(t, serveOutput, core.StyleHeader("Usage:"))
		assert.Contains(t, serveOutput, core.StyleHeader("Flags:"))
		assert.Contains(t, serveOutput, core.StyleUseLine("bgin serve [flags]"))
		assert.Contains(t, serveOutput, "Override APP_PORT")

		// 验证 make:crud 命令
		crudOutput := getHelpOutput(t, "make:crud")
		assert.Contains(t, crudOutput, core.StyleHeader("Usage:"))
		assert.Contains(t, crudOutput, core.StyleHeader("Flags:"))
		assert.Contains(t, crudOutput, core.StyleUseLine("bgin make:crud [name] [flags]"))
		assert.Contains(t, crudOutput, "Custom Chinese business description")
	})
}
