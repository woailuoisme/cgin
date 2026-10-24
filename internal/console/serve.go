package console

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"bgin/internal/bootstrap"
)

// ServeOptions 持有 HTTP 服务启动命令的配置与依赖容器 (SRP & DIP)。
type ServeOptions struct {
	Port    int
	Factory Factory
}

// NewServeOptions 构造 ServeOptions 实例。
func NewServeOptions(f Factory) *ServeOptions {
	return &ServeOptions{Factory: f}
}

// Complete 从命令行上下文解析补全选项。
func (o *ServeOptions) Complete(_ *cobra.Command, _ []string) error {
	return nil
}

// Validate 校验启动参数合法性。
func (o *ServeOptions) Validate() error {
	if o.Port < 0 || o.Port > 65535 {
		return &ValidationError{Field: "port", Message: "port must be between 0 and 65535"}
	}
	return nil
}

// Run 启动 HTTP 核心服务生命周期。
func (o *ServeOptions) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	application, err := bootstrap.NewApplication(ctx, o.Port)
	if err != nil {
		return err
	}
	defer application.Close()

	return application.Serve(ctx)
}

// newServeCmd 启动 HTTP 服务，对应 .air.toml 的 args_bin。
func newServeCmd(f Factory) *cobra.Command {
	opts := NewServeOptions(f)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.Complete(cmd, args); err != nil {
				return err
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			return opts.Run(cmd.Context())
		},
	}

	cmd.Flags().IntVarP(&opts.Port, "port", "p", 0, "Override APP_PORT (0 to use configured value)")

	return cmd
}
