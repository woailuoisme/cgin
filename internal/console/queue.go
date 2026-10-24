package console

import (
	"context"
	"fmt"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/dromara/carbon/v2"
	"github.com/hibiken/asynq"
	"github.com/spf13/cobra"

	"bgin/internal/bootstrap"
	"bgin/internal/console/core"
	"bgin/internal/features/client/auth"
	"bgin/internal/features/system"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/mailer"
	"bgin/internal/platform/queue"
	"bgin/internal/platform/storage"
)

// QueueWorkOptions 封装 queue:work 命令的参数与依赖容器 (SRP & DIP)。
type QueueWorkOptions struct {
	QueueArg         string
	Concurrency      int
	WithoutScheduler bool
	Factory          Factory
}

// NewQueueWorkOptions 构造 QueueWorkOptions 实例。
func NewQueueWorkOptions(f Factory) *QueueWorkOptions {
	return &QueueWorkOptions{
		QueueArg: queue.DefaultQueueName,
		Factory:  f,
	}
}

// Complete 从命令行上下文解析并补全配置。
func (o *QueueWorkOptions) Complete(_ *cobra.Command, _ []string) error {
	return nil
}

// Validate 校验 Worker 启动参数。
func (o *QueueWorkOptions) Validate() error {
	if o.Concurrency < 0 {
		return &ValidationError{Field: "concurrency", Message: "cannot be negative"}
	}
	return nil
}

// Run 启动 Asynq 工作协程与计划任务调度器。
func (o *QueueWorkOptions) Run(ctx context.Context) error {
	cfg, err := o.Factory.Config()
	if err != nil {
		return err
	}

	log := o.Factory.Logger()
	queue.SetFallbackLogger(log)

	mailerClient, err := mailer.New(cfg.Mail, log)
	if err != nil {
		return fmt.Errorf("failed to initialize mailer: %w", err)
	}
	defer func() { _ = mailerClient.Close() }()

	storageClient, err := storage.Open(ctx, cfg.Storage)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}

	reg := queue.DefaultRegistry()
	auth.RegisterJobs(reg, mailerClient)
	system.RegisterJobs(reg, storageClient)

	if !o.WithoutScheduler {
		sched := queue.NewSchedule(reg)
		bootstrap.RegisterSchedules(sched)
		sched.Register()
	}

	var locker queue.Locker
	if cacheLocker, err := cache.NewRedisLocker(cfg.Cache); err == nil {
		defer func() { _ = cacheLocker.Close() }()
		locker = queue.AdaptLocker(cacheLocker)
	}

	client, cleanup, err := queue.NewClient(cfg.Queue, locker)
	if err != nil {
		return fmt.Errorf("failed to initialize queue client: %w", err)
	}
	defer cleanup()
	client.SetLogger(log)

	concurrency := o.Concurrency
	if concurrency > 0 {
		client.SetConcurrency(concurrency)
	} else {
		concurrency = cfg.Queue.Concurrency
	}

	queues := ParseQueueWeights(o.QueueArg)

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if !o.WithoutScheduler {
		if err := client.StartScheduler(cfg); err != nil {
			return fmt.Errorf("failed to start schedule runner: %w", err)
		}
	}

	go func() {
		<-ctx.Done()
		_ = client.StopWorker()
		_ = client.StopScheduler()
	}()

	streams := o.Factory.IOStreams()
	core.TitleTo(streams.Out, fmt.Sprintf("Queue worker started [queues=%s concurrency=%d]...", o.QueueArg, concurrency))
	return client.RunWorker(queues)
}

// newQueueWorkCmd 启动 Asynq 统一工作协程与计划任务调度器。
func newQueueWorkCmd(f Factory) *cobra.Command {
	opts := NewQueueWorkOptions(f)

	cmd := &cobra.Command{
		Use:   "queue:work",
		Short: "Start processing jobs on the queue as a worker daemon",
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

	cmd.Flags().StringVarP(&opts.QueueArg, "queue", "q", queue.DefaultQueueName, "Names and weights of queues to listen to (e.g. 'default' or 'high:3,default:1')")
	cmd.Flags().IntVarP(&opts.Concurrency, "concurrency", "c", 0, "Maximum number of concurrent worker goroutines (0 to use config)")
	cmd.Flags().BoolVar(&opts.WithoutScheduler, "without-scheduler", false, "Disable the built-in recurring schedule runner in this worker")

	return cmd
}

// QueueFailedOptions 封装 queue:failed 命令的参数与依赖容器 (SRP & DIP)。
type QueueFailedOptions struct {
	Queue    string
	Page     int
	PageSize int
	Factory  Factory
}

// NewQueueFailedOptions 构造 QueueFailedOptions 实例。
func NewQueueFailedOptions(f Factory) *QueueFailedOptions {
	return &QueueFailedOptions{
		Page:     1,
		PageSize: 50,
		Factory:  f,
	}
}

// Complete 从命令行上下文解析并补全配置。
func (o *QueueFailedOptions) Complete(_ *cobra.Command, _ []string) error {
	o.Queue = strings.TrimSpace(o.Queue)
	return nil
}

// Validate 校验分页参数。
func (o *QueueFailedOptions) Validate() error {
	if o.Page <= 0 {
		return &ValidationError{Field: "page", Message: "must be greater than 0"}
	}
	if o.PageSize <= 0 {
		return &ValidationError{Field: "page-size", Message: "must be greater than 0"}
	}
	return nil
}

// Run 列出失败任务清单。
func (o *QueueFailedOptions) Run(_ context.Context) error {
	cfg, err := o.Factory.Config()
	if err != nil {
		return err
	}

	client, cleanup, err := queue.NewClient(cfg.Queue)
	if err != nil {
		return fmt.Errorf("failed to initialize queue client: %w", err)
	}
	defer cleanup()

	var tasks []*asynq.TaskInfo
	if o.Queue != "" {
		tasks, err = client.ListFailed(o.Queue, o.Page, o.PageSize)
	} else {
		tasks, err = client.ListFailedAll(o.Page, o.PageSize)
	}
	if err != nil {
		return fmt.Errorf("failed to query failed jobs: %w", err)
	}

	streams := o.Factory.IOStreams()
	if len(tasks) == 0 {
		core.InfoTo(streams.Out, "No failed jobs found.")
		return nil
	}

	w := tabwriter.NewWriter(streams.Out, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tQUEUE\tTASK\tERROR\tFAILED AT")
	for _, t := range tasks {
		errMsg := t.LastErr
		if len(errMsg) > 60 {
			errMsg = errMsg[:57] + "..."
		}
		failedAt := carbon.CreateFromStdTime(t.LastFailedAt)
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			t.ID,
			t.Queue,
			t.Type,
			errMsg,
			fmt.Sprintf("%s (%s)", failedAt.ToDateTimeString(), failedAt.DiffForHumans()),
		)
	}
	return w.Flush()
}

// newQueueFailedCmd 列出全部或指定队列中的失败任务。
func newQueueFailedCmd(f Factory) *cobra.Command {
	opts := NewQueueFailedOptions(f)

	cmd := &cobra.Command{
		Use:   "queue:failed",
		Short: "List all failed queue jobs",
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

	cmd.Flags().StringVarP(&opts.Queue, "queue", "q", "", "Filter failed jobs by queue name")
	cmd.Flags().IntVar(&opts.Page, "page", 1, "Page number to list")
	cmd.Flags().IntVar(&opts.PageSize, "page-size", 50, "Number of records per page")

	return cmd
}

// QueueRetryOptions 封装 queue:retry 命令的参数与依赖容器 (SRP & DIP)。
type QueueRetryOptions struct {
	Target  string
	Factory Factory
}

// NewQueueRetryOptions 构造 QueueRetryOptions 实例。
func NewQueueRetryOptions(f Factory) *QueueRetryOptions {
	return &QueueRetryOptions{Factory: f}
}

// Complete 从命令行上下文解析并补全配置。
func (o *QueueRetryOptions) Complete(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		o.Target = strings.TrimSpace(args[0])
	}
	return nil
}

// Validate 校验重试目标参数。
func (o *QueueRetryOptions) Validate() error {
	if o.Target == "" {
		return &ValidationError{Field: "target", Message: "please provide a job ID or 'all'"}
	}
	return nil
}

// Run 重新投递指定的失败任务或全部失败任务。
func (o *QueueRetryOptions) Run(_ context.Context) error {
	cfg, err := o.Factory.Config()
	if err != nil {
		return err
	}

	client, cleanup, err := queue.NewClient(cfg.Queue)
	if err != nil {
		return fmt.Errorf("failed to initialize queue client: %w", err)
	}
	defer cleanup()

	streams := o.Factory.IOStreams()
	if strings.EqualFold(o.Target, "all") {
		count, err := client.RetryFailedAll()
		if err != nil {
			return fmt.Errorf("failed to retry failed jobs: %w", err)
		}
		core.SuccessTo(streams.Out, "Retried [%d] failed jobs successfully.", count)
		return nil
	}

	if err := client.RetryFailed("", o.Target); err != nil {
		return fmt.Errorf("failed to retry job [%s]: %w", o.Target, err)
	}

	core.SuccessTo(streams.Out, "Failed job [%s] has been pushed back to the queue.", o.Target)
	return nil
}

// newQueueRetryCmd 重新投递指定的失败任务或全部失败任务。
func newQueueRetryCmd(f Factory) *cobra.Command {
	opts := NewQueueRetryOptions(f)

	return &cobra.Command{
		Use:   "queue:retry <id|all>",
		Short: "Retry a failed queue job by ID or 'all'",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
				return &ValidationError{Field: "target", Message: "please provide a job ID or 'all'"}
			}
			return nil
		},
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
}

// QueueFlushOptions 封装 queue:flush 命令的参数与依赖容器 (SRP & DIP)。
type QueueFlushOptions struct {
	Queue   string
	Force   bool
	Factory Factory
}

// NewQueueFlushOptions 构造 QueueFlushOptions 实例。
func NewQueueFlushOptions(f Factory) *QueueFlushOptions {
	return &QueueFlushOptions{Factory: f}
}

// Complete 从命令行上下文解析并补全配置。
func (o *QueueFlushOptions) Complete(_ *cobra.Command, _ []string) error {
	o.Queue = strings.TrimSpace(o.Queue)
	return nil
}

// Validate 校验清空命令选项合法性。
func (o *QueueFlushOptions) Validate() error {
	return nil
}

// Run 清空全部或指定队列中的失败任务。
func (o *QueueFlushOptions) Run(_ context.Context) error {
	cfg, err := o.Factory.Config()
	if err != nil {
		return err
	}

	streams := o.Factory.IOStreams()
	if !o.Force {
		promptText := "Are you sure you want to flush all failed jobs across all queues?"
		if o.Queue != "" {
			promptText = fmt.Sprintf("Are you sure you want to flush all failed jobs in [%s] queue?", o.Queue)
		}
		confirmed, err := o.Factory.Prompter().Confirm(
			promptText,
			"This operation will permanently purge all archived failed jobs.",
			"Yes, flush",
		)
		if err != nil {
			return err
		}
		if !confirmed {
			core.WarnTo(streams.Out, "Queue flush operation cancelled.")
			return core.ErrCanceled
		}
	}

	client, cleanup, err := queue.NewClient(cfg.Queue)
	if err != nil {
		return fmt.Errorf("failed to initialize queue client: %w", err)
	}
	defer cleanup()

	if o.Queue != "" {
		if err := client.PurgeArchived(o.Queue); err != nil {
			return fmt.Errorf("failed to flush jobs for queue [%s]: %w", o.Queue, err)
		}
		core.SuccessTo(streams.Out, "All failed jobs in queue [%s] have been flushed.", o.Queue)
		return nil
	}

	if err := client.PurgeArchivedAll(); err != nil {
		return fmt.Errorf("failed to flush all failed jobs: %w", err)
	}

	core.SuccessTo(streams.Out, "All failed jobs across all queues have been flushed.")
	return nil
}

// newQueueFlushCmd 清空所有失败任务。
func newQueueFlushCmd(f Factory) *cobra.Command {
	opts := NewQueueFlushOptions(f)

	cmd := &cobra.Command{
		Use:   "queue:flush",
		Short: "Flush all failed queue jobs",
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

	cmd.Flags().StringVarP(&opts.Queue, "queue", "q", "", "Filter queue to flush (empty for all queues)")
	cmd.Flags().BoolVarP(&opts.Force, core.FlagForce, "f", false, core.UsageForceFlag)

	return cmd
}

// ParseQueueWeights 解析队列权重参数字符串（如 "high:3,default:1" 或 "default"）。
func ParseQueueWeights(raw string) map[string]int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]int{queue.DefaultQueueName: 1}
	}

	result := make(map[string]int)
	for token := range strings.SplitSeq(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		name, weightStr, found := strings.Cut(token, ":")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		weight := 1
		if found {
			if w, err := strconv.Atoi(strings.TrimSpace(weightStr)); err == nil && w > 0 {
				weight = w
			}
		}
		result[name] = weight
	}

	if len(result) == 0 {
		return map[string]int{queue.DefaultQueueName: 1}
	}
	return result
}
