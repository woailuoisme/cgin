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

	"cgin/internal/bootstrap"
	"cgin/internal/job"
	"cgin/pkg/cache"
	"cgin/pkg/mailer"
	"cgin/pkg/queue"
)

// newServeCmd 启动 HTTP 服务，对应 .air.toml 的 args_bin。
func newServeCmd(_ Factory) *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP server",
		Args:  maxArgs(0),
		RunE: runE(func(ctx context.Context, _ []string) error {
			if port < 0 || port > 65535 {
				return &ValidationError{Field: "port", Message: "port must be between 0 and 65535"}
			}

			ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			application, err := bootstrap.NewApplication(ctx, port)
			if err != nil {
				return err
			}
			defer application.Close()

			return application.Serve(ctx)
		}),
	}

	cmd.Flags().IntVarP(&port, "port", "p", 0, "Override APP_PORT (0 to use configured value)")
	return cmd
}

// newQueueWorkCmd 启动 Asynq 统一工作协程与计划任务调度器。
func newQueueWorkCmd(f Factory) *cobra.Command {
	queueArg := queue.DefaultQueueName
	var concurrency int

	cmd := &cobra.Command{
		Use:   "queue:work",
		Short: "Start processing jobs on the queue as a worker daemon",
		Args:  maxArgs(0),
		RunE: runE(func(ctx context.Context, _ []string) error {
			if concurrency < 0 {
				return &ValidationError{Field: "concurrency", Message: "cannot be negative"}
			}

			cfg, err := f.Config()
			if err != nil {
				return err
			}

			log := f.Logger()

			mailerClient, err := mailer.New(cfg.Mail, log)
			if err != nil {
				return fmt.Errorf("failed to initialize mailer: %w", err)
			}
			defer func() { _ = mailerClient.Close() }()

			router := queue.NewTaskRouter()
			job.RegisterJobs(router, mailerClient)

			var locker queue.Locker
			if cacheLocker, err := cache.NewRedisLocker(cfg.Cache); err == nil {
				defer func() { _ = cacheLocker.Close() }()
				locker = queue.AdaptLocker(cacheLocker)
			}

			workerConcurrency := cfg.Queue.Concurrency
			if concurrency > 0 {
				workerConcurrency = concurrency
			}

			server, err := queue.NewServer(queue.Config{
				RedisURL:        cfg.Queue.RedisURL,
				Concurrency:     workerConcurrency,
				ShutdownTimeout: cfg.Queue.ShutdownTimeout,
			},
				queue.WithServerLogger(log),
				queue.WithServerLocker(locker),
			)
			if err != nil {
				return fmt.Errorf("failed to initialize queue server: %w", err)
			}
			defer func() { _ = server.Shutdown() }()

			scheduler, err := queue.NewScheduler(queue.Config{
				RedisURL: cfg.Queue.RedisURL,
			}, cfg.App.Location(), queue.WithSchedulerLogger(log))
			if err != nil {
				return fmt.Errorf("failed to initialize queue scheduler: %w", err)
			}
			defer func() { _ = scheduler.Shutdown() }()

			ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			go func() {
				<-ctx.Done()
				_ = scheduler.Shutdown()
				_ = server.Shutdown()
			}()

			if err := scheduler.Start(); err != nil {
				return fmt.Errorf("failed to start scheduler: %w", err)
			}

			f.IOStreams().Title("Queue worker started [queues=%s concurrency=%d]...", queueArg, workerConcurrency)
			return server.Run(ParseQueueWeights(queueArg), router)
		}),
	}

	cmd.Flags().StringVarP(&queueArg, "queue", "q", queue.DefaultQueueName, "Names and weights of queues to listen to (e.g. 'default' or 'high:3,default:1')")
	cmd.Flags().IntVarP(&concurrency, "concurrency", "c", 0, "Maximum number of concurrent worker goroutines (0 to use config)")
	return cmd
}

func openQueueInspector(f Factory) (queue.Inspector, error) {
	cfg, err := f.Config()
	if err != nil {
		return nil, err
	}
	inspector, err := queue.NewInspector(queue.Config{
		RedisURL: cfg.Queue.RedisURL,
	}, queue.WithInspectorLogger(f.Logger()))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize queue inspector: %w", err)
	}
	return inspector, nil
}

// newQueueFailedCmd 列出全部或指定队列中的失败任务。
func newQueueFailedCmd(f Factory) *cobra.Command {
	var (
		queueName string
		page      = 1
		pageSize  = 50
	)

	cmd := &cobra.Command{
		Use:   "queue:failed",
		Short: "List all failed queue jobs",
		Args:  maxArgs(0),
		RunE: runE(func(_ context.Context, _ []string) error {
			if page <= 0 {
				return &ValidationError{Field: "page", Message: "must be greater than 0"}
			}
			if pageSize <= 0 {
				return &ValidationError{Field: "page-size", Message: "must be greater than 0"}
			}

			client, err := openQueueInspector(f)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()

			var tasks []*asynq.TaskInfo
			if q := strings.TrimSpace(queueName); q != "" {
				tasks, err = client.ListFailed(q, page, pageSize)
			} else {
				tasks, err = client.ListFailedAll(page, pageSize)
			}
			if err != nil {
				return fmt.Errorf("failed to query failed jobs: %w", err)
			}

			streams := f.IOStreams()
			if len(tasks) == 0 {
				streams.Info("No failed jobs found.")
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
		}),
	}

	cmd.Flags().StringVarP(&queueName, "queue", "q", "", "Filter failed jobs by queue name")
	cmd.Flags().IntVar(&page, "page", 1, "Page number to list")
	cmd.Flags().IntVar(&pageSize, "page-size", 50, "Number of records per page")
	return cmd
}

// newQueueRetryCmd 重新投递指定的失败任务或全部失败任务。
func newQueueRetryCmd(f Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "queue:retry <id|all>",
		Short: "Retry a failed queue job by ID or 'all'",
		Args:  maxArgs(1),
		RunE: runE(func(_ context.Context, args []string) error {
			target := firstArg(args)
			if target == "" {
				return &ValidationError{Field: "target", Message: "please provide a job ID or 'all'"}
			}

			client, err := openQueueInspector(f)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()

			streams := f.IOStreams()
			if strings.EqualFold(target, "all") {
				count, err := client.RetryFailedAll()
				if err != nil {
					return fmt.Errorf("failed to retry failed jobs: %w", err)
				}
				streams.Success("Retried [%d] failed jobs successfully.", count)
				return nil
			}

			if err := client.RetryFailed("", target); err != nil {
				return fmt.Errorf("failed to retry job [%s]: %w", target, err)
			}
			streams.Success("Failed job [%s] has been pushed back to the queue.", target)
			return nil
		}),
	}
}

// newQueueFlushCmd 清空所有失败任务。
func newQueueFlushCmd(f Factory) *cobra.Command {
	var (
		queueName string
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "queue:flush",
		Short: "Flush all failed queue jobs",
		Args:  maxArgs(0),
		RunE: runE(func(_ context.Context, _ []string) error {
			q := strings.TrimSpace(queueName)
			promptText := "Are you sure you want to flush all failed jobs across all queues?"
			if q != "" {
				promptText = fmt.Sprintf("Are you sure you want to flush all failed jobs in [%s] queue?", q)
			}
			if err := confirmOrCancel(
				f,
				force,
				promptText,
				"This operation will permanently purge all archived failed jobs.",
				"Yes, flush",
				"Queue flush operation cancelled.",
			); err != nil {
				return err
			}

			client, err := openQueueInspector(f)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()

			streams := f.IOStreams()
			if q != "" {
				if err := client.PurgeArchived(q); err != nil {
					return fmt.Errorf("failed to flush jobs for queue [%s]: %w", q, err)
				}
				streams.Success("All failed jobs in queue [%s] have been flushed.", q)
				return nil
			}

			if err := client.PurgeArchivedAll(); err != nil {
				return fmt.Errorf("failed to flush all failed jobs: %w", err)
			}
			streams.Success("All failed jobs across all queues have been flushed.")
			return nil
		}),
	}

	cmd.Flags().StringVarP(&queueName, "queue", "q", "", "Filter queue to flush (empty for all queues)")
	cmd.Flags().BoolVarP(&force, FlagForce, "f", false, UsageForceFlag)
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
