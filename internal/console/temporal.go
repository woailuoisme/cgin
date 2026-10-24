package console

import (
	"cmp"
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"bgin/internal/console/core"
	"bgin/internal/features/system"
	"bgin/internal/platform/storage"
	"bgin/internal/platform/temporal"
)

// TemporalWorkOptions 封装 temporal:work 命令的执行参数与依赖容器 (SRP & DIP)。
type TemporalWorkOptions struct {
	TaskQueue   string
	Namespace   string
	Concurrency int
	Force       bool
	Factory     Factory
}

// NewTemporalWorkOptions 构造 TemporalWorkOptions 实例。
func NewTemporalWorkOptions(f Factory) *TemporalWorkOptions {
	return &TemporalWorkOptions{
		Factory: f,
	}
}

// Complete 从命令行上下文解析并补全配置。
func (o *TemporalWorkOptions) Complete(_ *cobra.Command, _ []string) error {
	return nil
}

// Validate 校验 Worker 启动参数。
func (o *TemporalWorkOptions) Validate() error {
	if o.Concurrency < 0 {
		return &ValidationError{Field: "concurrency", Message: "cannot be negative"}
	}
	return nil
}

// Run 启动 Temporal Worker 监听并消费工作流与活动任务。
func (o *TemporalWorkOptions) Run(ctx context.Context) error {
	cfg, err := o.Factory.Config()
	if err != nil {
		return err
	}

	streams := o.Factory.IOStreams()

	// 检查 Temporal 开关状态
	if !cfg.Temporal.Enabled && !o.Force {
		core.WarnTo(streams.Out, "Temporal is not enabled.")
		core.InfoTo(streams.Out, "Please set TEMPORAL_ENABLED=true in your .env file or run with --force to enable Temporal workflows and worker.")
		return nil
	}

	log := o.Factory.Logger()

	// 解析并应用覆盖参数
	taskQueue := cmp.Or(o.TaskQueue, cfg.Temporal.TaskQueue)
	cfg.Temporal.Namespace = cmp.Or(o.Namespace, cfg.Temporal.Namespace)
	if o.Concurrency > 0 {
		cfg.Temporal.ActivityConcurrency = o.Concurrency
	}

	storageClient, err := storage.Open(ctx, cfg.Storage)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}

	// 1. 初始化并连接 Temporal 集群客户端
	client, err := temporal.NewClient(cfg.Temporal, log, cfg.Telemetry.Enabled)
	if err != nil {
		return fmt.Errorf("failed to initialize temporal client: %w", err)
	}
	defer client.Close()

	// 2. 构造指定任务队列的 Worker 实例
	w := temporal.NewWorker(client, taskQueue, cfg.Temporal)

	// 3. 注册各垂直领域切片的 Workflows 与 Activities (领域模块自注册)
	system.RegisterTemporal(w, storageClient)

	// 4. 启动 Worker 监听消费
	if err := w.Start(); err != nil {
		return fmt.Errorf("failed to start temporal worker: %w", err)
	}

	core.TitleTo(streams.Out, fmt.Sprintf("Temporal worker started [queue=%s namespace=%s concurrency=%d]...",
		taskQueue, cfg.Temporal.Namespace, cfg.Temporal.ActivityConcurrency))

	log.Info("temporal worker started successfully",
		zap.String("task_queue", taskQueue),
		zap.String("namespace", cfg.Temporal.Namespace),
		zap.Int("activity_concurrency", cfg.Temporal.ActivityConcurrency),
	)

	// 5. 监听平滑退出信号
	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-sigCtx.Done()
	log.Info("stopping temporal worker...")
	w.Stop()
	log.Info("temporal worker stopped")

	return nil
}

// newTemporalWorkCmd 创建 temporal:work 控制台命令。
func newTemporalWorkCmd(f Factory) *cobra.Command {
	opts := NewTemporalWorkOptions(f)

	cmd := &cobra.Command{
		Use:   "temporal:work",
		Short: "Start processing workflows and activities on Temporal as a worker daemon",
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

	cmd.Flags().StringVarP(&opts.TaskQueue, "task-queue", "q", "", "Temporal task queue name (defaults to config)")
	cmd.Flags().StringVarP(&opts.Namespace, "namespace", "n", "", "Temporal namespace (defaults to config)")
	cmd.Flags().IntVarP(&opts.Concurrency, "concurrency", "c", 0, "Maximum number of concurrent activity executions (0 to use config)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Force start worker even if TEMPORAL_ENABLED is false in config")

	return cmd
}
