// Package console 是 cgin 的命令行界面：命令定义、参数解析与终端输出。
//
// 应用的装配与运行在 cgin/internal/bootstrap，命令只做参数校验与编排，
// 依赖方向是 console → bootstrap。
package console

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dromara/carbon/v2"
	"github.com/spf13/cobra"

	"cgin/internal/console/core"
)

// 构建元信息，由链接期 -X 注入。
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// ErrCanceled 表示用户在交互式确认中主动放弃了操作。
// 顶层 CLI 入口可通过 errors.Is(err, console.ErrCanceled) 拦截并以退出码 0 优雅退出。
var ErrCanceled = core.ErrCanceled

// UsageError 标记命令行用法错误（标志解析失败或参数语义校验不通过）。
// 顶层 CLI 入口据此以退出码 2 结束，且不写入运行日志。
type UsageError struct {
	// CommandPath 是触发错误的完整命令路径，用于提示对应的 --help。
	CommandPath string
	// Err 是被包装的原始校验错误。
	Err error
}

// Error 返回底层错误的原始描述。
func (e *UsageError) Error() string { return e.Err.Error() }

// Unwrap 暴露底层错误，便于 errors.Is / errors.As 穿透判定。
func (e *UsageError) Unwrap() error { return e.Err }

// SetVersion 供 main 注入构建元信息。
func SetVersion(v, c, d string) {
	version, commit, date = v, c, d
}

// Execute 解析命令行并执行选中的命令。
func Execute(ctx context.Context, args []string) error {
	return ExecuteWithFactory(ctx, NewDefaultFactory(), args)
}

// ExecuteWithFactory 注入依赖工厂，解析命令行并执行选中的命令。
func ExecuteWithFactory(ctx context.Context, f Factory, args []string) error {
	cmd := NewRootCommandWithFactory(f)
	// 如果传入包含可执行程序名自身的 argv（如 os.Args），切掉程序名
	if len(args) > 0 && (args[0] == "cgin" || filepath.Base(args[0]) == filepath.Base(os.Args[0])) {
		args = args[1:]
	}
	cmd.SetArgs(args)
	return cmd.ExecuteContext(ctx)
}

// NewRootCommand 组装根命令与全部子命令，使用默认依赖工厂。
func NewRootCommand() *cobra.Command {
	return NewRootCommandWithFactory(NewDefaultFactory())
}

// NewRootCommandWithFactory 注入依赖工厂并组装根命令与全部子命令。
func NewRootCommandWithFactory(f Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "cgin",
		Short:         "cgin application console",
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if streams := f.IOStreams(); streams != nil {
				streams.Out = cmd.OutOrStdout()
				streams.ErrOut = cmd.ErrOrStderr()
				streams.In = cmd.InOrStdin()
			}

			// 配置在此处集中加载一次，供碳时间库与各命令的 Factory 共用。
			cfg, err := f.Config()
			if err != nil {
				// 刻意不阻断：route list、make:* 等命令无需完整配置即可工作。
				// 但配置损坏会让时区与语言静默退回默认值，必须让运维看见。
				core.WarnTo(cmd.ErrOrStderr(),
					"warning: load config failed, timezone and locale fall back to defaults: %v", err)
				return nil
			}
			if cfg == nil {
				return nil
			}
			carbon.SetTimezone(cfg.App.Timezone)
			carbon.SetLocale(cfg.I18n.DefaultLanguage)
			return nil
		},
	}

	// flag 解析错误会沿父命令回溯，只需在根命令统一包装为用法错误。
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &UsageError{CommandPath: c.CommandPath(), Err: err}
	})

	// 帮助与用法模板（色彩能力来自注入的 IOStreams）
	core.SetupColoredHelp(cmd, f.IOStreams())

	// Artisan 风格命令：名字里的冒号前缀即分组，分组随命令自动派生，无需另行维护清单。
	commands := []*cobra.Command{
		newAppInitCmd(f),
		newServeCmd(f),
		newDBMigrateCmd(f),
		newDBSeedCmd(f),
		newDBFreshCmd(f),
		newGeoIPUpdateCmd(f),
		newJWTSecretCmd(f),
		newMakeCRUDCmd(f),
		newQueueWorkCmd(f),
		newQueueFailedCmd(f),
		newQueueRetryCmd(f),
		newQueueFlushCmd(f),
		newRouteListCmd(f),
		newScoutImportCmd(f),
		newScoutFlushCmd(f),
		newDITreeCmd(f),
	}
	registerCommandGroups(cmd, commands)

	cmd.AddCommand(commands...)

	return cmd
}

// registerCommandGroups 按命令名前缀建立分组（保持首次出现顺序），并回填每个命令的 GroupID。
func registerCommandGroups(root *cobra.Command, commands []*cobra.Command) {
	seen := make(map[string]struct{}, len(commands))
	for _, subCmd := range commands {
		prefix, _, ok := strings.Cut(subCmd.Name(), ":")
		if !ok {
			continue
		}

		subCmd.GroupID = prefix
		if _, exists := seen[prefix]; exists {
			continue
		}
		seen[prefix] = struct{}{}
		root.AddGroup(&cobra.Group{ID: prefix, Title: prefix})
	}
}

// commandOptions 统一 CLI 命令的补全、校验与执行三段式生命周期。
// 仅有 Validate 的命令可内嵌 noopComplete，反之则内嵌 noopValidate。
type commandOptions interface {
	// Complete 从位置参数补齐命令配置。
	Complete(args []string) error
	// Validate 校验参数语义。
	Validate() error
	// Run 执行命令主体。
	Run(context.Context) error
}

// noopComplete 为不接受位置参数补全的命令提供默认实现。
type noopComplete struct{}

// Complete 是空实现。
func (noopComplete) Complete([]string) error { return nil }

// noopValidate 为无需额外语义校验的命令提供默认实现。
type noopValidate struct{}

// Validate 是空实现。
func (noopValidate) Validate() error { return nil }

// firstArg 返回首个已去除首尾空白的位置参数，缺失时为空字符串。
func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.TrimSpace(args[0])
}

// maxArgs 构造限定位置参数上界的 cobra 校验器。
func maxArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > n {
			return &UsageError{
				CommandPath: cmd.CommandPath(),
				Err:         fmt.Errorf("accepts at most %d arg(s), received %d", n, len(args)),
			}
		}
		return nil
	}
}

// runCommand 将三段式生命周期收敛为统一的 cobra RunE。
func runCommand(opts commandOptions) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := opts.Complete(args); err != nil {
			return err
		}
		if err := opts.Validate(); err != nil {
			return &UsageError{CommandPath: cmd.CommandPath(), Err: err}
		}
		return opts.Run(cmd.Context())
	}
}
