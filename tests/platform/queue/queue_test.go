// Package queue_test 验证任务队列基础设施与调度编排的核心契约。
package queue_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"bgin/internal/config"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/queue"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/redis/go-redis/v9/maintnotifications.(*CircuitBreakerManager).cleanupLoop"),
	)
}

type dummyJob struct {
	queue.JobMeta
	Payload string `json:"payload"`
}

func (j *dummyJob) Handle(_ context.Context) error {
	return nil
}

func TestJobMetaDefaultsAndChaining(t *testing.T) {
	// 测试默认属性
	meta := queue.NewJobMeta("send:email")
	assert.Equal(t, "send:email", meta.TaskName())
	assert.Equal(t, queue.DefaultQueueName, meta.QueueName())
	assert.Equal(t, 0, meta.MaxRetries())
	assert.Zero(t, meta.Timeout())
	assert.Zero(t, meta.Retention())

	ttl, enabled := meta.WithoutOverlapping()
	assert.False(t, enabled)
	assert.Zero(t, ttl)

	// 测试链式配置
	custom := meta.
		WithRetries(5).
		WithTimeout(15 * time.Second).
		WithRetention(2 * time.Hour).
		WithWithoutOverlapping(3 * time.Minute)

	assert.Equal(t, 5, custom.MaxRetries())
	assert.Equal(t, 15*time.Second, custom.Timeout())
	assert.Equal(t, 2*time.Hour, custom.Retention())

	ttl, enabled = custom.WithoutOverlapping()
	assert.True(t, enabled)
	assert.Equal(t, 3*time.Minute, ttl)

	// 测试未传 ttl 时的默认兜底（10分钟）
	defaultLock := meta.WithWithoutOverlapping()
	ttl, enabled = defaultLock.WithoutOverlapping()
	assert.True(t, enabled)
	assert.Equal(t, queue.DefaultScheduleLockTTL, ttl)
}

func TestResolveQueueName(t *testing.T) {
	jobWithCustomQueue := &dummyJob{
		JobMeta: queue.NewJobMeta("custom:job", "critical"),
	}
	assert.Equal(t, "critical", queue.ResolveQueueName(jobWithCustomQueue))

	jobWithDefaultQueue := &dummyJob{
		JobMeta: queue.NewJobMeta("default:job"),
	}
	assert.Equal(t, queue.DefaultQueueName, queue.ResolveQueueName(jobWithDefaultQueue))
}

func TestRegistryJobAndCron(t *testing.T) {
	reg := queue.NewSchedule().Entries()
	assert.Empty(t, reg)

	// 注册合法任务工厂
	queue.DefaultRegistry().Job(func() queue.Job {
		return &dummyJob{JobMeta: queue.NewJobMeta("registered:task")}
	})

	// 校验空工厂 panic
	assert.Panics(t, func() {
		queue.DefaultRegistry().Job(nil)
	})

	assert.Panics(t, func() {
		queue.DefaultRegistry().Job(func() queue.Job {
			return nil
		})
	})

	// 注册 Cron 并获取
	job := &dummyJob{JobMeta: queue.NewJobMeta("cron:task")}
	queue.DefaultRegistry().Cron("*/5 * * * *", job)

	entries := queue.GetCronEntries()
	require.NotEmpty(t, entries)
	found := false
	for _, e := range entries {
		if e.Job.TaskName() == "cron:task" && e.Spec == "*/5 * * * *" {
			found = true
			break
		}
	}
	assert.True(t, found, "应当能检索到已注册的 Cron 条目")
}

func TestScheduleCronExpressions(t *testing.T) {
	sched := queue.NewSchedule()
	job := &dummyJob{JobMeta: queue.NewJobMeta("report:generate")}

	tests := []struct {
		name     string
		entry    *queue.ScheduleEntry
		expected string
	}{
		{"EveryMinute", sched.Job(job).EveryMinute(), "* * * * *"},
		{"EveryTwoMinutes", sched.Job(job).EveryTwoMinutes(), "*/2 * * * *"},
		{"EveryFiveMinutes", sched.Job(job).EveryFiveMinutes(), "*/5 * * * *"},
		{"EveryTenMinutes", sched.Job(job).EveryTenMinutes(), "*/10 * * * *"},
		{"EveryFifteenMinutes", sched.Job(job).EveryFifteenMinutes(), "*/15 * * * *"},
		{"EveryThirtyMinutes", sched.Job(job).EveryThirtyMinutes(), "*/30 * * * *"},
		{"Hourly", sched.Job(job).Hourly(), "0 * * * *"},
		{"HourlyAt", sched.Job(job).HourlyAt(20), "20 * * * *"},
		{"Daily", sched.Job(job).Daily(), "0 0 * * *"},
		{"DailyAt", sched.Job(job).DailyAt("04:15"), "15 4 * * *"},
		{"Weekly", sched.Job(job).Weekly(), "0 0 * * 0"},
		{"WeeklyOn", sched.Job(job).WeeklyOn(time.Friday, "18:00"), "0 18 * * 5"},
		{"Monthly", sched.Job(job).Monthly(), "0 0 1 * *"},
		{"MonthlyOn", sched.Job(job).MonthlyOn(10, "09:30"), "30 9 10 * *"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.entry.Spec())
			assert.True(t, tt.entry.IsValid(), "Cron 表达式必须有效")
			next, err := tt.entry.NextRun()
			require.NoError(t, err)
			assert.True(t, next.After(time.Now()))
		})
	}
}

// TestScheduleInvalidTimeWarns 锁定非法调度时间"不注册且可见告警"。
//
// 这条同时是回归防线：队列包的内部告警走包级兜底日志器，若它退回 Nop，
// 非法配置会被静默吞掉——修复就形同虚设。
func TestScheduleInvalidTimeWarns(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	queue.SetFallbackLogger(zap.New(core))
	t.Cleanup(func() { queue.SetFallbackLogger(nil) })

	sched := queue.NewSchedule()
	entry := sched.Job(&dummyJob{JobMeta: queue.NewJobMeta("report:bad-time")}).DailyAt("25:00")

	require.False(t, entry.IsValid())
	require.Equal(t, 1, logs.Len(), "非法调度时间必须留下一条 Error 告警")
	assert.Contains(t, logs.All()[0].Message, "invalid schedule time")
}

// TestScheduleOptionalDefaultTime 锁定"省略时间即默认零点"的便利语义。
func TestScheduleOptionalDefaultTime(t *testing.T) {
	sched := queue.NewSchedule()
	job := &dummyJob{JobMeta: queue.NewJobMeta("report:weekly")}

	assert.Equal(t, "0 0 * * 5", sched.Job(job).WeeklyOn(time.Friday).Spec())
	assert.Equal(t, "0 0 10 * *", sched.Job(job).MonthlyOn(10).Spec())
	assert.Equal(t, "0 0 10 * *", sched.Job(job).MonthlyOn(10, "").Spec())
}

// TestScheduleRejectsInvalidTime 锁定非法入参不得静默退化为 00:00。
//
// 退化会让配置写错的定时任务在错误时间执行且毫无提示；正确行为是留下空 spec
// （Register 阶段跳过并告警），并让配置阶段即可由 IsValid 观察到失败。
func TestScheduleRejectsInvalidTime(t *testing.T) {
	newEntry := func(name string) *queue.ScheduleEntry {
		return queue.NewSchedule().Job(&dummyJob{JobMeta: queue.NewJobMeta(name)})
	}

	tests := []struct {
		name  string
		entry *queue.ScheduleEntry
	}{
		{"DailyAt 小时越界", newEntry("t1").DailyAt("25:00")},
		{"DailyAt 分钟越界", newEntry("t2").DailyAt("12:60")},
		{"DailyAt 非时间串", newEntry("t3").DailyAt("abc")},
		{"DailyAt 缺分钟", newEntry("t4").DailyAt("04")},
		{"HourlyAt 越界", newEntry("t5").HourlyAt(99)},
		{"WeeklyOn 时间非法", newEntry("t6").WeeklyOn(time.Friday, "1:99")},
		{"WeeklyOn 星期越界", newEntry("t7").WeeklyOn(time.Weekday(9))},
		{"MonthlyOn 日期越界", newEntry("t8").MonthlyOn(0)},
		{"MonthlyOn 时间非法", newEntry("t9").MonthlyOn(10, "abc")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Empty(t, tt.entry.Spec(), "非法入参不得退化成任何可执行表达式")
			assert.False(t, tt.entry.IsValid())
		})
	}
}

func TestScheduleEntryAttributes(t *testing.T) {
	sched := queue.NewSchedule()
	job := &dummyJob{JobMeta: queue.NewJobMeta("sync:data")}

	entry := sched.Job(job).
		EveryFiveMinutes().
		OnQueue("batch").
		WithoutOverlapping(5 * time.Minute)

	assert.Equal(t, "batch", entry.Queue())
	assert.True(t, entry.IsWithoutOverlapping())
	assert.Equal(t, 5*time.Minute, entry.LockTTL())
	assert.Equal(t, job, entry.Job())

	// 注册幂等性验证
	sched.Register()
	sched.Register() // 第二次调用不应重复注册或报错
}

// mockLocker 实现用于单元测试的内存互斥锁。
type mockLocker struct {
	acquireFn func(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error)
}

func (m *mockLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error) {
	if m.acquireFn != nil {
		return m.acquireFn(ctx, key, ttl)
	}
	return func(context.Context) error { return nil }, true, nil
}

func TestTryLockBehavior(t *testing.T) {
	ctx := t.Context()

	// 1. 无 locker 时直接执行
	executed := false
	ok, err := queue.TryLock(ctx, nil, "key", time.Minute, func(_ context.Context) error {
		executed = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.True(t, executed)

	// 2. 获取锁成功并执行 fn，验证 release 被调用
	released := false
	lockerSuccess := &mockLocker{
		acquireFn: func(_ context.Context, _ string, _ time.Duration) (func(context.Context) error, bool, error) {
			return func(_ context.Context) error {
				released = true
				return nil
			}, true, nil
		},
	}
	executed = false
	ok, err = queue.TryLock(ctx, lockerSuccess, "key", time.Minute, func(_ context.Context) error {
		executed = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.True(t, executed)
	assert.True(t, released, "锁释放函数必须在 defer 中被调用")

	// 3. 锁被占用跳过执行
	lockerBusy := &mockLocker{
		acquireFn: func(_ context.Context, _ string, _ time.Duration) (func(context.Context) error, bool, error) {
			return nil, false, nil
		},
	}
	executed = false
	ok, err = queue.TryLock(ctx, lockerBusy, "key", time.Minute, func(_ context.Context) error {
		executed = true
		return nil
	})
	require.NoError(t, err)
	assert.False(t, ok, "锁被占用应返回 acquired=false")
	assert.False(t, executed, "锁被占用时不得执行任务")

	// 4. 获取锁发生底层网络或协议错误
	expectedErr := errors.New("redis connection refused")
	lockerError := &mockLocker{
		acquireFn: func(_ context.Context, _ string, _ time.Duration) (func(context.Context) error, bool, error) {
			return nil, false, expectedErr
		},
	}
	ok, err = queue.TryLock(ctx, lockerError, "key", time.Minute, func(_ context.Context) error {
		return nil
	})
	require.ErrorIs(t, err, expectedErr)
	assert.False(t, ok)
}

func TestWrapOverlappingJob(t *testing.T) {
	ctx := t.Context()
	var runCount int32

	job := &countingJob{
		JobMeta: queue.NewJobMeta("counting:job"),
		counter: &runCount,
	}

	// 模拟已加锁状态
	locked := false
	locker := &mockLocker{
		acquireFn: func(_ context.Context, _ string, _ time.Duration) (func(context.Context) error, bool, error) {
			if locked {
				return nil, false, nil
			}
			locked = true
			return func(_ context.Context) error {
				locked = false
				return nil
			}, true, nil
		},
	}

	wrapped := queue.WrapOverlappingJob(job, 5*time.Minute, locker)
	assert.Equal(t, "counting:job", wrapped.TaskName())

	lockProvider, isLockProvider := wrapped.(queue.OverlappingLockProvider)
	require.True(t, isLockProvider)
	ttl, enabled := lockProvider.WithoutOverlapping()
	assert.True(t, enabled)
	assert.Equal(t, 5*time.Minute, ttl)

	// 首次调用，应正常执行
	require.NoError(t, wrapped.Handle(ctx))
	assert.Equal(t, int32(1), atomic.LoadInt32(&runCount))

	// 当锁被占用时，调用应跳过执行并不返回错误
	locked = true
	require.NoError(t, wrapped.Handle(ctx))
	assert.Equal(t, int32(1), atomic.LoadInt32(&runCount), "锁被占用时不应增加执行计数")
}

type countingJob struct {
	queue.JobMeta
	counter *int32
}

func (j *countingJob) Handle(_ context.Context) error {
	atomic.AddInt32(j.counter, 1)
	return nil
}

func TestClientConcurrencyAndShutdownTimeout(t *testing.T) {
	c, cleanup, err := queue.NewClient(config.QueueConfig{
		RedisURL:        "redis://127.0.0.1:6379",
		Concurrency:     5,
		ShutdownTimeout: 15 * time.Second,
	})
	require.NoError(t, err)
	defer cleanup()

	assert.Equal(t, 5, c.Concurrency())
	assert.Equal(t, 15*time.Second, c.ShutdownTimeout())

	c.SetConcurrency(20)
	assert.Equal(t, 20, c.Concurrency())

	c.SetShutdownTimeout(30 * time.Second)
	assert.Equal(t, 30*time.Second, c.ShutdownTimeout())
}

func TestClientConcurrencyAndShutdownTimeout_Default(t *testing.T) {
	c, cleanup, err := queue.NewClient(config.QueueConfig{
		RedisURL: "redis://127.0.0.1:6379",
	})
	require.NoError(t, err)
	defer cleanup()

	assert.Equal(t, 10, c.Concurrency())
	assert.Equal(t, 3*time.Second, c.ShutdownTimeout())
}

func TestAdaptLocker(t *testing.T) {
	ctx := t.Context()

	// 1. 空接口处理
	assert.Nil(t, queue.AdaptLocker(nil))

	// 2. 正常适配 cache.MemoryLocker
	memLocker := cache.NewMemoryLocker()
	defer func() { _ = memLocker.Close() }()

	adapted := queue.AdaptLocker(memLocker)
	require.NotNil(t, adapted)

	const lockKey = "order:1001"

	// 第一次 Acquire 成功
	release1, ok1, err1 := adapted.Acquire(ctx, lockKey, time.Minute)
	require.NoError(t, err1)
	assert.True(t, ok1)
	require.NotNil(t, release1)

	// 第二次获取相同 key 被占用拒绝
	release2, ok2, err2 := adapted.Acquire(ctx, lockKey, time.Minute)
	require.NoError(t, err2)
	assert.False(t, ok2)
	assert.Nil(t, release2)

	// 释放第一次锁
	err := release1(ctx)
	require.NoError(t, err)

	// 释放后应能再次成功抢占
	release3, ok3, err3 := adapted.Acquire(ctx, lockKey, time.Minute)
	require.NoError(t, err3)
	assert.True(t, ok3)
	require.NotNil(t, release3)
	_ = release3(ctx)
}
