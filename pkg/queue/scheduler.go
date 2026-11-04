package queue

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/hibiken/asynq"

	"cgin/pkg/logger"
)

// CronEntry 表示已登记的周期性任务条目。
type CronEntry struct {
	Spec     string
	TaskName string
	Payload  any
	Settings TaskSettings
}

// Scheduler 专职负责基于标准 Cron 表达式的周期任务编排与派发 (SRP)。
type Scheduler struct {
	redisOpt asynq.RedisConnOpt
	location *time.Location
	logger   *slog.Logger

	mu        sync.RWMutex
	scheduler *asynq.Scheduler
	entries   []CronEntry
	started   bool
}

// SchedulerOptions 调度器构建选项。
type SchedulerOptions struct {
	Logger *slog.Logger
}

// SchedulerOption 函数式选项配置。
type SchedulerOption func(*SchedulerOptions)

// WithSchedulerLogger 注入调度器日志输出器。
func WithSchedulerLogger(l *slog.Logger) SchedulerOption {
	return func(o *SchedulerOptions) {
		o.Logger = l
	}
}

// NewScheduler 创建周期调度器实例。
func NewScheduler(cfg Config, loc *time.Location, opts ...SchedulerOption) (*Scheduler, error) {
	norm := cfg.Normalized()
	redisOpt, err := ParseRedisURI(norm.RedisURL)
	if err != nil {
		return nil, err
	}

	var opt SchedulerOptions
	for _, fn := range opts {
		if fn != nil {
			fn(&opt)
		}
	}

	return &Scheduler{
		redisOpt: redisOpt,
		location: cmp.Or(loc, time.UTC),
		logger:   cmp.Or(opt.Logger, logger.Nop()),
	}, nil
}

// Register 登记一条周期调度任务。
// 支持标准 5 段式 Cron 表达式（如 "0 2 * * *"）与标准宏（如 "@daily"）。
func (s *Scheduler) Register(spec string, taskName string, payload any, opts ...TaskOption) error {
	if s == nil {
		return errors.New("queue: scheduler is nil")
	}
	if spec == "" {
		return errors.New("queue: cron spec cannot be empty")
	}
	if taskName == "" {
		return errors.New("queue: task name cannot be empty")
	}

	settings := BuildTaskSettings(opts...)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = append(s.entries, CronEntry{
		Spec:     spec,
		TaskName: taskName,
		Payload:  payload,
		Settings: settings,
	})

	// 如果调度器已经启动，动态向底层注册
	if s.started && s.scheduler != nil {
		return s.registerUnderlying(spec, taskName, payload, settings)
	}

	return nil
}

func (s *Scheduler) registerUnderlying(spec, taskName string, payload any, settings TaskSettings) error {
	envBytes, err := packEnvelope(context.Background(), taskName, payload)
	if err != nil {
		return fmt.Errorf("queue: failed to pack envelope for periodic task %q: %w", taskName, err)
	}

	task := asynq.NewTask(taskName, envBytes)
	asynqOpts := settings.AsynqOptions()

	entryID, err := s.scheduler.Register(spec, task, asynqOpts...)
	if err != nil {
		return fmt.Errorf("queue: failed to register periodic task %q (%s): %w", taskName, spec, err)
	}

	s.logger.Info("periodic task registered successfully",
		slog.String("task", taskName),
		slog.String("spec", spec),
		slog.String("queue", settings.QueueName()),
		slog.String("entry_id", entryID),
	)

	return nil
}

// Start 启动周期调度引擎。
func (s *Scheduler) Start() error {
	if s == nil {
		return errors.New("queue: scheduler is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return nil
	}

	if len(s.entries) == 0 {
		s.logger.Debug("periodic task scheduler skipped, no entries registered")
		return nil
	}

	sched := asynq.NewScheduler(s.redisOpt, &asynq.SchedulerOpts{
		Location: s.location,
	})
	s.scheduler = sched

	for _, entry := range s.entries {
		if err := s.registerUnderlying(entry.Spec, entry.TaskName, entry.Payload, entry.Settings); err != nil {
			s.logger.Error("failed to register periodic entry during startup",
				slog.String("task", entry.TaskName),
				slog.String("spec", entry.Spec),
				slog.Any("error", err),
			)
		}
	}

	if err := sched.Start(); err != nil {
		return fmt.Errorf("queue: failed to start scheduler: %w", err)
	}

	s.started = true
	s.logger.Info("periodic task scheduler started successfully",
		slog.Int("total_entries", len(s.entries)),
		slog.String("location", s.location.String()),
	)

	return nil
}

// Shutdown 优雅关闭周期调度器。
func (s *Scheduler) Shutdown() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started || s.scheduler == nil {
		return nil
	}

	s.scheduler.Shutdown()
	s.started = false
	s.scheduler = nil
	s.logger.Info("periodic task scheduler stopped")

	return nil
}

// Entries 返回所有已配置的周期调度条目只读切片。
func (s *Scheduler) Entries() []CronEntry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.entries)
}
