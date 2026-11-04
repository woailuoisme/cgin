package queue

import (
	"cmp"
	"time"

	"github.com/hibiken/asynq"
)

// DefaultMaxRetries 默认最大重试次数。
const DefaultMaxRetries = 3

// DefaultLockTTL 默认防重叠锁时长（10分钟）。
const DefaultLockTTL = 10 * time.Minute

// TaskSettings 聚合任务派发与消费时的控制属性。
type TaskSettings struct {
	Queue              string
	Delay              time.Duration
	ProcessAt          time.Time
	MaxRetry           int
	Timeout            time.Duration
	Retention          time.Duration
	TaskID             string
	WithoutOverlapping bool
	LockTTL            time.Duration
}

// TaskOption 函数式任务选项契约。
type TaskOption func(*TaskSettings)

// WithQueue 指定任务派发的目标队列名称。
func WithQueue(queueName string) TaskOption {
	return func(s *TaskSettings) {
		s.Queue = queueName
	}
}

// WithDelay 延迟指定时长后再将任务推送到待执行队列。
func WithDelay(delay time.Duration) TaskOption {
	return func(s *TaskSettings) {
		s.Delay = delay
	}
}

// WithProcessAt 指定任务未来执行的具体时间点。
func WithProcessAt(t time.Time) TaskOption {
	return func(s *TaskSettings) {
		s.ProcessAt = t
	}
}

// WithMaxRetry 设置任务执行失败后的最大重试次数。
func WithMaxRetry(retries int) TaskOption {
	return func(s *TaskSettings) {
		s.MaxRetry = retries
	}
}

// WithTimeout 设置任务单次执行的超时时间。
func WithTimeout(timeout time.Duration) TaskOption {
	return func(s *TaskSettings) {
		s.Timeout = timeout
	}
}

// WithRetention 设置任务执行完成后的结果保留时长。
func WithRetention(retention time.Duration) TaskOption {
	return func(s *TaskSettings) {
		s.Retention = retention
	}
}

// WithTaskID 显式指定任务唯一标识符（支持外部自定义幂等去重键）。
func WithTaskID(id string) TaskOption {
	return func(s *TaskSettings) {
		s.TaskID = id
	}
}

// WithoutOverlapping 为任务开启基于分布式锁的防并发重叠保护。
func WithoutOverlapping(ttl ...time.Duration) TaskOption {
	return func(s *TaskSettings) {
		s.WithoutOverlapping = true
		if len(ttl) > 0 && ttl[0] > 0 {
			s.LockTTL = ttl[0]
		} else {
			s.LockTTL = DefaultLockTTL
		}
	}
}

// BuildTaskSettings 将多个 TaskOption 编译为 TaskSettings 实体。
func BuildTaskSettings(opts ...TaskOption) TaskSettings {
	s := TaskSettings{
		Queue:    DefaultQueueName,
		MaxRetry: DefaultMaxRetries,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&s)
		}
	}
	return s
}

// AsynqOptions 将 TaskSettings 转化为底层 asynq.Option 集合。
func (s TaskSettings) AsynqOptions() []asynq.Option {
	out := make([]asynq.Option, 0, 6)

	if s.Queue != "" {
		out = append(out, asynq.Queue(s.Queue))
	}
	if s.MaxRetry > 0 {
		out = append(out, asynq.MaxRetry(s.MaxRetry))
	}
	if s.Timeout > 0 {
		out = append(out, asynq.Timeout(s.Timeout))
	}
	if s.Retention > 0 {
		out = append(out, asynq.Retention(s.Retention))
	}
	if s.TaskID != "" {
		out = append(out, asynq.TaskID(s.TaskID))
	}
	if s.Delay > 0 {
		out = append(out, asynq.ProcessIn(s.Delay))
	} else if !s.ProcessAt.IsZero() {
		out = append(out, asynq.ProcessAt(s.ProcessAt))
	}

	return out
}

// QueueName 获取当前任务设置的队列名称。
func (s TaskSettings) QueueName() string {
	return cmp.Or(s.Queue, DefaultQueueName)
}
