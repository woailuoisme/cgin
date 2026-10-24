// Package ready 提供基础设施组件就绪探针调度、状态降级与聚合评估底座。
package ready

import (
	"context"
	"errors"
	"time"
)

// 常用基础设施组件名称常量，用于规范 checks 映射键名与链路日志。
const (
	ComponentDatabase = "database"
	ComponentCache    = "cache"
	ComponentStorage  = "storage"
	ComponentSearch   = "search"
	ComponentRealtime = "realtime"
	ComponentImgproxy = "imgproxy"
	ComponentMailer   = "mailer"
)

// ErrProbeNotConfigured 表示探测实例未注入或未初始化。
var ErrProbeNotConfigured = errors.New("probe not configured")

// Pinger 约束支持上下文 Ping 探测的基础设施组件。
type Pinger interface {
	Ping(ctx context.Context) error
}

// CheckFunc 定义上下文感知的探测函数契约。
type CheckFunc func(ctx context.Context) error

// Check 描述单个就绪探测项的配置与执行规范。
type Check struct {
	Name     string
	Ping     CheckFunc
	Critical bool
	Driver   string
	Timeout  time.Duration
	Enabled  func() bool
}

// IsEnabled 报告该检查项当前是否处于启用探测状态。
func (c Check) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return c.Enabled()
}

// CheckOption 定义 Check 的函数式配置项。
type CheckOption func(*Check)

// WithCritical 指定探测项是否属于核心阻断级关键依赖。
func WithCritical(critical bool) CheckOption {
	return func(c *Check) {
		c.Critical = critical
	}
}

// WithDriver 设置底层驱动名称（如 postgres、valkey、s3 等）。
func WithDriver(driver string) CheckOption {
	return func(c *Check) {
		c.Driver = driver
	}
}

// WithTimeout 设置该检查项独享的超时时限。
func WithTimeout(timeout time.Duration) CheckOption {
	return func(c *Check) {
		c.Timeout = timeout
	}
}

// WithEnabled 设置组件启用状态探查函数。若返回 false，聚合器跳过探测并标记为 disabled。
func WithEnabled(enabled func() bool) CheckOption {
	return func(c *Check) {
		c.Enabled = enabled
	}
}

// NewCheck 构造具备可选配置的探测项规范。
func NewCheck(name string, ping CheckFunc, opts ...CheckOption) Check {
	c := Check{
		Name: name,
		Ping: ping,
	}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// PingerFunc 将标准 Pinger 实例安全适配为 CheckFunc。若 pinger 为 nil，则返回未就绪错误。
func PingerFunc(p Pinger) CheckFunc {
	return func(ctx context.Context) error {
		if p == nil {
			return ErrProbeNotConfigured
		}
		return p.Ping(ctx)
	}
}
