// Package console 是 cgin 的命令行界面：命令定义、参数解析与终端输出。
//
// 应用的装配与运行在 cgin/internal/bootstrap，命令只做参数校验与编排，
// 依赖方向是 console → bootstrap。
package console

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"
	"github.com/samber/do/v2"
	"github.com/spf13/cobra"

	"cgin/internal/bootstrap"
	"cgin/internal/config"
	"cgin/internal/database"
	"cgin/pkg/logger"
)

// 构建元信息，由链接期 -X 注入。
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const defaultAppName = "cgin"

// ErrCanceled 表示用户在交互式确认中主动放弃了操作。
// 顶层 CLI 入口可通过 errors.Is(err, console.ErrCanceled) 拦截并以退出码 0 优雅退出。
var ErrCanceled = errors.New("operation canceled by user")

// UsageError 标记命令行用法错误（标志解析失败或参数语义校验不通过）。
// 顶层 CLI 入口据此以退出码 2 结束，且不写入运行日志。
type UsageError struct {
	CommandPath string
	Err         error
}

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// ValidationError 表示控制台命令参数校验失败的强类型错误。
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return e.Field + ": " + e.Message
	}
	return e.Message
}

// Factory 按需提供控制台命令所需的外部依赖与基础设施（由 samber/do 容器管理懒加载生命周期）。
type Factory interface {
	IOStreams() *IOStreams
	Prompter() Prompter
	Config() (*config.Config, error)
	DB(ctx context.Context) (*database.DB, error)
	Logger() *zerolog.Logger
	Injector() *do.RootScope
}

type defaultFactory struct {
	ioStreams *IOStreams
	prompter  Prompter
	injector  *do.RootScope
}

// NewDefaultFactory 构造生产环境默认的懒加载依赖工厂实例。
func NewDefaultFactory() Factory {
	streams := SystemIOStreams()
	return &defaultFactory{
		ioStreams: streams,
		prompter:  NewHuhPrompter(streams.CanPrompt),
		injector:  bootstrap.NewInjector(),
	}
}

func (f *defaultFactory) IOStreams() *IOStreams { return f.ioStreams }
func (f *defaultFactory) Prompter() Prompter    { return f.prompter }
func (f *defaultFactory) Config() (*config.Config, error) {
	return do.Invoke[*config.Config](f.injector)
}
func (f *defaultFactory) DB(_ context.Context) (*database.DB, error) {
	return do.Invoke[*database.DB](f.injector)
}
func (f *defaultFactory) Injector() *do.RootScope { return f.injector }
func (f *defaultFactory) Logger() *zerolog.Logger {
	if log, err := do.Invoke[*zerolog.Logger](f.injector); err == nil {
		return log
	}
	return logger.Nop()
}

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
	if len(args) > 0 && (args[0] == defaultAppName || filepath.Base(args[0]) == filepath.Base(os.Args[0])) {
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
		Use:           defaultAppName,
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
			return nil
		},
	}

	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &UsageError{CommandPath: c.CommandPath(), Err: err}
	})

	SetupColoredHelp(cmd, f.IOStreams())

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

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.TrimSpace(args[0])
}

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

// runE 统一包装命令执行函数，自动将 ValidationError 转换为带命令路径的 UsageError（退出码 2）。
func runE(fn func(ctx context.Context, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := fn(cmd.Context(), args); err != nil {
			if _, ok := errors.AsType[*ValidationError](err); ok {
				return &UsageError{CommandPath: cmd.CommandPath(), Err: err}
			}
			return err
		}
		return nil
	}
}

func confirmOrCancel(f Factory, force bool, title, description, affirmative, cancelMsg string) error {
	if force {
		return nil
	}
	confirmed, err := f.Prompter().Confirm(title, description, affirmative)
	if err != nil {
		return err
	}
	if !confirmed {
		f.IOStreams().Warn("%s", cancelMsg)
		return ErrCanceled
	}
	return nil
}
