package queue

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/samber/lo"
)

const (
	DefaultQueueName = "default"
	DefaultTaskName  = "default:task"
	// DefaultMaxRetries 是任务未自声明重试预算时的兜底次数。
	// 显式兜底以免静默继承 Asynq 的 25 次指数退避：重试预算属于任务契约，必须可推断。
	DefaultMaxRetries = 3
)

// Job 描述队列任务或定时任务的标准执行契约。
type Job interface {
	TaskName() string
	Handle(ctx context.Context) error
}

// QueueNameProvider 为任务声明默认队列名的可选接口。
type QueueNameProvider interface {
	QueueName() string
}

// ResolveQueueName 获取任务的目标队列名，未显式声明时回退为默认队列名。
func ResolveQueueName(job Job) string {
	if qp, ok := job.(QueueNameProvider); ok {
		return cmp.Or(qp.QueueName(), DefaultQueueName)
	}
	return DefaultQueueName
}

// MaxRetriesProvider 允许任务声明默认重试次数上限。
type MaxRetriesProvider interface {
	MaxRetries() int
}

// TimeoutProvider 允许任务声明执行超时时间。
type TimeoutProvider interface {
	Timeout() time.Duration
}

// RetentionProvider 允许任务声明执行完成后的保留时长。
type RetentionProvider interface {
	Retention() time.Duration
}

// OverlappingLockProvider 允许任务自声明防并发重叠锁的超时时间。
type OverlappingLockProvider interface {
	WithoutOverlapping() (time.Duration, bool)
}

// DefaultScheduleLockTTL 声明防并发重叠任务默认锁超时时长（10分钟）。
const DefaultScheduleLockTTL = 10 * time.Minute

// JobMeta 封装后台任务执行元数据与调度约束（不参与载荷序列化）。
type JobMeta struct {
	Type           string        `json:"-"`
	Queue          string        `json:"-"`
	Retries        int           `json:"-"`
	TimeoutVal     time.Duration `json:"-"`
	RetentionVal   time.Duration `json:"-"`
	WithoutOverlap bool          `json:"-"`
	LockTTLVal     time.Duration `json:"-"`
}

// NewJobMeta 构建一个标准的 JobMeta 任务元数据实例。
func NewJobMeta(jobType string, queueName ...string) JobMeta {
	return JobMeta{
		Type:  jobType,
		Queue: cmp.Or(lo.FirstOrEmpty(queueName), DefaultQueueName),
	}
}

// TaskName 返回任务名称标识。
func (m JobMeta) TaskName() string {
	return cmp.Or(m.Type, DefaultTaskName)
}

// QueueName 返回目标队列名称。
func (m JobMeta) QueueName() string {
	return cmp.Or(m.Queue, DefaultQueueName)
}

// MaxRetries 返回任务允许的最大重试次数；返回 0 表示未声明，由 Dispatch 补齐默认值。
func (m JobMeta) MaxRetries() int {
	return m.Retries
}

// Timeout 返回任务的单次执行超时时间。
func (m JobMeta) Timeout() time.Duration {
	return m.TimeoutVal
}

// Retention 返回任务执行完成后的结果保留时长。
func (m JobMeta) Retention() time.Duration {
	return m.RetentionVal
}

// WithRetries 返回设置了最大重试次数的 JobMeta 副本。
func (m JobMeta) WithRetries(retries int) JobMeta {
	m.Retries = retries
	return m
}

// WithTimeout 返回设置了执行超时时间的 JobMeta 副本。
func (m JobMeta) WithTimeout(timeout time.Duration) JobMeta {
	m.TimeoutVal = timeout
	return m
}

// WithRetention 返回设置了结果保留时长的 JobMeta 副本。
func (m JobMeta) WithRetention(retention time.Duration) JobMeta {
	m.RetentionVal = retention
	return m
}

// WithoutOverlapping 返回是否开启防并发重叠锁及锁超时时长。
func (m JobMeta) WithoutOverlapping() (time.Duration, bool) {
	return m.LockTTLVal, m.WithoutOverlap
}

// WithWithoutOverlapping 为任务开启基于分布式锁的防并发重叠保护。
func (m JobMeta) WithWithoutOverlapping(ttl ...time.Duration) JobMeta {
	m.WithoutOverlap = true
	m.LockTTLVal = cmp.Or(lo.FirstOrEmpty(ttl), DefaultScheduleLockTTL)
	return m
}

// CronEntry 表示已注册的定时周期调度任务配置。
type CronEntry struct {
	Spec string
	Job  Job
	Opts []asynq.Option
}

// Registry 负责集中注册任务工厂与 Cron 周期调度规则。
//
// 处理器注册收工厂而不是实例：worker 每次执行都需要一个元数据完整的新实例，
// 由 Job 的构造器给出（而不是反射复制原型），因此类型在编译期确定、不再有静默丢弃。
type Registry interface {
	// Job 注册任务工厂；工厂返回的实例自带 JobMeta，payload 由 worker 反序列化填充。
	Job(factory func() Job)
	// Cron 登记一条周期调度规则。这里接收的是声明实例而非工厂：该实例是调度载荷的来源，
	// 因此可以携带调用方给定的参数（如清理目录），处理器则由 Job 工厂负责注册。
	Cron(spec string, job Job, opts ...asynq.Option)
	// SetJobLockTTL 在注册表中配置任务名称级别的防重叠锁超时时间。
	SetJobLockTTL(taskName string, ttl time.Duration)
	// Get 查询指定任务名绑定的任务构造工厂。
	Get(taskName string) (func() Job, bool)
}

// QueueStatus 表示队列当前运行状态的统计指标。
type QueueStatus struct {
	Name      string
	Pending   int
	Running   int
	Succeeded int
	Failed    int
	Scheduled int
	Retry     int
	Archived  int
	Paused    bool
}

type jobRegistry struct {
	jobs        map[string]func() Job
	cronEntries []CronEntry
	jobLocks    map[string]time.Duration
	mu          sync.RWMutex
}

var _ Registry = (*jobRegistry)(nil)

var defaultReg = &jobRegistry{
	jobs:     make(map[string]func() Job),
	jobLocks: make(map[string]time.Duration),
}

// DefaultRegistry 返回全局单例任务与调度注册中心。
func DefaultRegistry() Registry {
	return defaultReg
}

func (r *jobRegistry) Job(factory func() Job) {
	if factory == nil {
		panic("queue: nil job factory registered")
	}

	probe := factory()
	if probe == nil {
		panic("queue: job factory returned nil instance")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.jobs[probe.TaskName()] = factory
}

// Get 查询已注册的任务构造工厂。
func (r *jobRegistry) Get(taskName string) (func() Job, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	f, ok := r.jobs[taskName]
	return f, ok
}

func (r *jobRegistry) Cron(spec string, job Job, opts ...asynq.Option) {
	if job == nil || spec == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.cronEntries = append(r.cronEntries, CronEntry{
		Spec: spec,
		Job:  job,
		Opts: opts,
	})
}

// SetJobLockTTL 在注册表中配置指定任务名对应的防重叠锁超时时间。
func (r *jobRegistry) SetJobLockTTL(taskName string, ttl time.Duration) {
	if taskName == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.jobLocks == nil {
		r.jobLocks = make(map[string]time.Duration)
	}
	r.jobLocks[taskName] = ttl
}

// LookupLockTTL 查找任务的防重叠锁配置（优先尊重 Job 自声明，次之查询注册表）。
func (r *jobRegistry) LookupLockTTL(taskName string, job Job) (time.Duration, bool) {
	if olp, ok := job.(OverlappingLockProvider); ok {
		if ttl, enabled := olp.WithoutOverlapping(); enabled {
			return ttl, true
		}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	ttl, ok := r.jobLocks[taskName]
	return ttl, ok
}

// GetCronEntries 返回所有已注册的定时周期任务切片副本。
func GetCronEntries() []CronEntry {
	defaultReg.mu.RLock()
	defer defaultReg.mu.RUnlock()

	return slices.Clone(defaultReg.cronEntries)
}
