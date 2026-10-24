// Package console 是 bgin 的命令行界面：命令定义、参数解析与终端输出。
//
// 应用的装配与运行在 bgin/internal/bootstrap，命令只做参数校验与编排，
// 依赖方向是 console → bootstrap（见 ADR-0002）。
package console

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dromara/carbon/v2"
	"github.com/spf13/cobra"

	"bgin/internal/config"
	"bgin/internal/console/core"
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
	if len(args) > 0 && (args[0] == "bgin" || filepath.Base(args[0]) == filepath.Base(os.Args[0])) {
		args = args[1:]
	}
	cmd.SetArgs(args)
	return cmd.ExecuteContext(ctx)
}

// NewRootCommand 组装根命令与全部子命令，使用默认依赖工厂。
func NewRootCommand() *cobra.Command {
	return NewRootCommandWithFactory(NewDefaultFactory())
}

// NewRootCommandWithFactory 注入依赖工厂并组装根命令与全部子命令 (IoC & DIP)。
func NewRootCommandWithFactory(f Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "bgin",
		Short:         "bgin application console",
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if streams := f.IOStreams(); streams != nil {
				streams.Out = cmd.OutOrStdout()
				streams.ErrOut = cmd.ErrOrStderr()
				streams.In = cmd.InOrStdin()
			}

			cfg, err := config.Load()
			if err != nil {
				// 刻意不阻断：route list、make:* 等命令无需完整配置即可工作。
				// 但配置损坏会让时区与语言静默退回默认值，必须让运维看见。
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: load config failed, timezone and locale fall back to defaults: %v\n", err)
				return nil
			}
			carbon.SetTimezone(cfg.App.Timezone)
			carbon.SetLocale(cfg.I18n.DefaultLanguage)
			return nil
		},
	}

	// 注册色彩丰富且对齐严密的终端帮助与用法模板
	core.SetupColoredHelp(cmd)

	// 注册以命名空间命名的业务命令分类组（Laravel Artisan 风格分组）
	for _, id := range []string{"db", "geoip", "jwt", "make", "queue", "route", "scout", "sentry", "temporal"} {
		cmd.AddGroup(&cobra.Group{ID: id, Title: id})
	}

	// 挂载纯冒号 Artisan 风格一等公民命令
	commands := []*cobra.Command{
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
		newSentryTestCmd(f),
		newTemporalWorkCmd(f),
	}

	for _, subCmd := range commands {
		if prefix, _, ok := strings.Cut(subCmd.Name(), ":"); ok {
			subCmd.GroupID = prefix
		}
	}

	cmd.AddCommand(commands...)

	return cmd
}
