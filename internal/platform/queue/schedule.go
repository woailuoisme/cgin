package queue

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adhocore/gronx"
	"github.com/hibiken/asynq"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

// Schedule 集中管理和编排周期性定时调度任务（类似 Laravel Console Kernel Schedule）。
type Schedule struct {
	registry Registry
	entries  []*ScheduleEntry
	mu       sync.Mutex
	once     sync.Once
}

// NewSchedule 创建具有指定注册表的周期性任务调度构建器。
func NewSchedule(registries ...Registry) *Schedule {
	return &Schedule{
		registry: cmp.Or(lo.FirstOrEmpty(registries), DefaultRegistry()),
		entries:  make([]*ScheduleEntry, 0),
	}
}

// Job 添加一个待编排调度的任务实例。类型在编译期确定，非任务对象无法进入调度编排。
func (s *Schedule) Job(job Job) *ScheduleEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := &ScheduleEntry{
		job:      job,
		schedule: s,
	}

	s.entries = append(s.entries, entry)
	return entry
}

// Entries 返回所有已配置的调度条目切片。
func (s *Schedule) Entries() []*ScheduleEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.entries)
}

// Register 将所有已编排的调度规则正式编译并挂载到底层队列注册表中。
// 使用内部 once 保证单个 Schedule 实例只执行一次编译与挂载，具备幂等防重保障。
func (s *Schedule) Register() {
	s.once.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		for _, entry := range s.entries {
			if entry.job == nil {
				continue
			}
			if entry.spec == "" {
				// 未配置执行频率的条目（例如忘了链式调用 Daily/EveryXxx）：留痕而非静默跳过。
				// 非法时间/表达式已在配置阶段报错，走不到这里。
				fallbackLog().Warn("schedule entry has no frequency, skipped",
					zap.String("task", entry.job.TaskName()))
				continue
			}

			if !gronx.IsValid(entry.spec) {
				fallbackLog().Error("invalid cron expression, skipped schedule entry",
					zap.String("task", entry.job.TaskName()),
					zap.String("spec", entry.spec))
				continue
			}

			if entry.withoutOverlapping {
				s.registry.SetJobLockTTL(entry.job.TaskName(), entry.lockTTL)
			}

			opts := entry.opts
			if entry.queueName != "" {
				opts = append(opts, asynq.Queue(entry.queueName))
			}

			s.registry.Cron(entry.spec, entry.job, opts...)
		}
	})
}

// ScheduleEntry 描述单个周期任务的调度配置、执行频率与并发约束。
type ScheduleEntry struct {
	job                Job
	spec               string
	queueName          string
	withoutOverlapping bool
	lockTTL            time.Duration
	opts               []asynq.Option
	schedule           *Schedule
}

// Cron 直接使用标准 5 字段 Cron 表达式或别名配置执行周期。
func (e *ScheduleEntry) Cron(spec string) *ScheduleEntry {
	e.spec = spec
	return e
}

// EveryMinute 配置为每分钟执行一次（"* * * * *"）。
func (e *ScheduleEntry) EveryMinute() *ScheduleEntry {
	return e.Cron("* * * * *")
}

// EveryTwoMinutes 配置为每两分钟执行一次（"*/2 * * * *"）。
func (e *ScheduleEntry) EveryTwoMinutes() *ScheduleEntry {
	return e.Cron("*/2 * * * *")
}

// EveryFiveMinutes 配置为每五分钟执行一次（"*/5 * * * *"）。
func (e *ScheduleEntry) EveryFiveMinutes() *ScheduleEntry {
	return e.Cron("*/5 * * * *")
}

// EveryTenMinutes 配置为每十分钟执行一次（"*/10 * * * *"）。
func (e *ScheduleEntry) EveryTenMinutes() *ScheduleEntry {
	return e.Cron("*/10 * * * *")
}

// EveryFifteenMinutes 配置为每十五分钟执行一次（"*/15 * * * *"）。
func (e *ScheduleEntry) EveryFifteenMinutes() *ScheduleEntry {
	return e.Cron("*/15 * * * *")
}

// EveryThirtyMinutes 配置为每三十分钟执行一次（"*/30 * * * *"）。
func (e *ScheduleEntry) EveryThirtyMinutes() *ScheduleEntry {
	return e.Cron("*/30 * * * *")
}

// Hourly 配置为每小时整点执行一次（"0 * * * *"）。
func (e *ScheduleEntry) Hourly() *ScheduleEntry {
	return e.Cron("0 * * * *")
}

// HourlyAt 配置为每小时指定分钟执行（如 HourlyAt(15) 表示每小时第 15 分钟）。越界视为配置错误。
func (e *ScheduleEntry) HourlyAt(minute int) *ScheduleEntry {
	if minute < 0 || minute > 59 {
		return e.invalidSpec("HourlyAt", strconv.Itoa(minute))
	}
	return e.Cron(fmt.Sprintf("%d * * * *", minute))
}

// Daily 配置为每天午夜 00:00 执行一次（"0 0 * * *"）。
func (e *ScheduleEntry) Daily() *ScheduleEntry {
	return e.Cron("0 0 * * *")
}

// DailyAt 配置为每天指定时间执行（入参格式支持 "HH:MM"，如 "02:00", "14:30"）。格式非法视为配置错误。
func (e *ScheduleEntry) DailyAt(timeStr string) *ScheduleEntry {
	hour, minute, ok := parseTimeHM(timeStr)
	if !ok {
		return e.invalidSpec("DailyAt", timeStr)
	}
	return e.Cron(fmt.Sprintf("%d %d * * *", minute, hour))
}

// Weekly 配置为每周日午夜 00:00 执行一次（"0 0 * * 0"）。
func (e *ScheduleEntry) Weekly() *ScheduleEntry {
	return e.Cron("0 0 * * 0")
}

// WeeklyOn 配置为每周指定星期几执行；at 可省略（默认 00:00，格式 "HH:MM"）。星期与时间非法均视为配置错误。
func (e *ScheduleEntry) WeeklyOn(day time.Weekday, at ...string) *ScheduleEntry {
	if day < time.Sunday || day > time.Saturday {
		return e.invalidSpec("WeeklyOn", strconv.Itoa(int(day)))
	}
	hour, minute, ok := parseOptionalTimeHM(at...)
	if !ok {
		return e.invalidSpec("WeeklyOn", lo.FirstOrEmpty(at))
	}
	return e.Cron(fmt.Sprintf("%d %d * * %d", minute, hour, day))
}

// Monthly 配置为每月 1 号午夜 00:00 执行一次（"0 0 1 * *"）。
func (e *ScheduleEntry) Monthly() *ScheduleEntry {
	return e.Cron("0 0 1 * *")
}

// MonthlyOn 配置为每月指定日期执行（day 取值 1-31，越界视为配置错误）；at 可省略（默认 00:00）。
func (e *ScheduleEntry) MonthlyOn(day int, at ...string) *ScheduleEntry {
	if day < 1 || day > 31 {
		return e.invalidSpec("MonthlyOn", strconv.Itoa(day))
	}
	hour, minute, ok := parseOptionalTimeHM(at...)
	if !ok {
		return e.invalidSpec("MonthlyOn", lo.FirstOrEmpty(at))
	}
	return e.Cron(fmt.Sprintf("%d %d %d * *", minute, hour, day))
}

// OnQueue 指定任务推入的目标队列名称（如 "critical", "low"）。
func (e *ScheduleEntry) OnQueue(queueName string) *ScheduleEntry {
	e.queueName = queueName
	return e
}

// WithoutOverlapping 启用防并发重叠锁，防止上一次周期未跑完时下一次并发执行。
// 可选参数 ttl 为锁超时时间，默认兜底 10 分钟。
func (e *ScheduleEntry) WithoutOverlapping(ttl ...time.Duration) *ScheduleEntry {
	e.withoutOverlapping = true
	e.lockTTL = cmp.Or(lo.FirstOrEmpty(ttl), DefaultScheduleLockTTL)
	return e
}

// WithOptions 追加自定义 asynq 调度选项。
func (e *ScheduleEntry) WithOptions(opts ...asynq.Option) *ScheduleEntry {
	e.opts = append(e.opts, opts...)
	return e
}

// Spec 返回编译后的 Cron 周期表达式。
func (e *ScheduleEntry) Spec() string {
	return e.spec
}

// IsValid 使用 gronx 校验当前条目的 Cron 周期表达式是否合法。
func (e *ScheduleEntry) IsValid() bool {
	return gronx.IsValid(e.spec)
}

// NextRun 计算当前 Cron 表达式在指定时间（默认当前时间）后的下一次触发时间。
func (e *ScheduleEntry) NextRun(after ...time.Time) (time.Time, error) {
	if len(after) > 0 {
		return gronx.NextTickAfter(e.spec, after[0], false)
	}
	return gronx.NextTick(e.spec, false)
}

// Queue 返回指定的目标队列名。
func (e *ScheduleEntry) Queue() string {
	return e.queueName
}

// IsWithoutOverlapping 判断是否启用了防重叠锁。
func (e *ScheduleEntry) IsWithoutOverlapping() bool {
	return e.withoutOverlapping
}

// LockTTL 返回防重叠锁的超时时间。
func (e *ScheduleEntry) LockTTL() time.Duration {
	return e.lockTTL
}

// Job 返回当前条目所绑定的任务。
func (e *ScheduleEntry) Job() Job {
	return e.job
}

// overlappingJobWrapper 为实现了防并发重叠机制的任务包装器。
type overlappingJobWrapper struct {
	inner   Job
	lockTTL time.Duration
	locker  Locker
	logger  *zap.Logger
}

var _ Job = (*overlappingJobWrapper)(nil)

// WrapOverlappingJob 为指定任务附加基于基础设施分布式锁的防并发重叠执行保护。
// locker 为 nil 时退化为直接执行（无锁基础设施的部署形态）。
func WrapOverlappingJob(job Job, ttl time.Duration, locker Locker) Job {
	return WrapOverlappingJobWithLogger(nil, job, ttl, locker)
}

// WrapOverlappingJobWithLogger 同上，并显式注入结构性日志器，避免绕过调用方的日志通道。
func WrapOverlappingJobWithLogger(l *zap.Logger, job Job, ttl time.Duration, locker Locker) Job {
	return &overlappingJobWrapper{
		inner:   job,
		lockTTL: cmp.Or(ttl, DefaultScheduleLockTTL),
		locker:  locker,
		logger:  l,
	}
}

// log 返回注入的日志器，未注入时退回包级兜底实例。
func (w *overlappingJobWrapper) log() *zap.Logger {
	if w.logger != nil {
		return w.logger
	}
	return fallbackLog()
}

// TaskName 返回被包装任务的唯一标识名。
func (w *overlappingJobWrapper) TaskName() string {
	return w.inner.TaskName()
}

// WithoutOverlapping 返回防并发重叠锁的超时时间并标记已启用。
func (w *overlappingJobWrapper) WithoutOverlapping() (time.Duration, bool) {
	return w.lockTTL, true
}

// Handle 执行被包装任务，并在执行前后通过分布式锁确保同一时刻只有一个实例在运行。
//
// 错误传播的两种情形都在下面第一个分支里：TryLock 在锁基础设施故障时返回 (false, err)，
// 在抢到锁时返回 (true, 内层任务的执行结果)，两者都必须原样上抛。
func (w *overlappingJobWrapper) Handle(ctx context.Context) error {
	if w.locker == nil {
		// 无锁基础设施时直接执行
		return w.inner.Handle(ctx)
	}

	lockKey := "schedule:lock:" + w.inner.TaskName()
	acquired, err := TryLock(ctx, w.locker, lockKey, w.lockTTL, func(lockCtx context.Context) error {
		return w.inner.Handle(lockCtx)
	})

	// 锁基础设施故障必须先于「未抢到锁」判定：两者都返回 acquired=false，
	// 但前者是灾难性错误，静默跳过会让任务既不执行也不告警。
	if err != nil {
		return err
	}
	if !acquired {
		w.log().Warn("scheduled task skipped due to active overlap lock",
			zap.String("task", w.inner.TaskName()),
			zap.String("lock_key", lockKey),
		)
		return nil
	}

	return nil
}

// parseOptionalTimeHM 解析可选的 "HH:MM"。
// 未提供（或空串）视为"使用默认零点"并返回 ok=true；提供了但格式非法返回 ok=false。
func parseOptionalTimeHM(at ...string) (hour, min int, ok bool) {
	if len(at) == 0 || strings.TrimSpace(at[0]) == "" {
		return 0, 0, true
	}
	return parseTimeHM(at[0])
}

// parseTimeHM 解析 "HH:MM" 格式的时间字符串。
// ok=false 表示格式非法或越界——调用方必须据此拒绝该调度，而不是退化成 00:00。
func parseTimeHM(timeStr string) (hour, min int, ok bool) {
	hStr, mStr, found := strings.Cut(strings.TrimSpace(timeStr), ":")
	if !found {
		return 0, 0, false
	}

	parsedHour, err1 := strconv.Atoi(strings.TrimSpace(hStr))
	parsedMin, err2 := strconv.Atoi(strings.TrimSpace(mStr))
	if err1 != nil || err2 != nil || parsedHour < 0 || parsedHour > 23 || parsedMin < 0 || parsedMin > 59 {
		return 0, 0, false
	}

	return parsedHour, parsedMin, true
}

// invalidSpec 把配置错误的调度条目置为"不可注册"并当场告警。
//
// 空 spec 会被 Register 跳过，因此非法入参既不会在错误时间执行任务，也不会静默消失：
// 配置阶段就有一条可见的告警指出哪个 API 收到了什么非法值。
//
// 该状态不粘滞：之后任何一次成功的调度配置调用（Cron/EveryXxx）都会覆盖 spec，
// 那属于调用方显式重新指定，先前的告警已如实记录过一次失败。
func (e *ScheduleEntry) invalidSpec(api, value string) *ScheduleEntry {
	e.spec = ""
	fallbackLog().Error("invalid schedule time, entry will not be registered",
		zap.String("api", api),
		zap.String("value", value),
	)
	return e
}
