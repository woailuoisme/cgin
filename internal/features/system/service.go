package system

import (
	"context"

	"bgin/internal/platform/ready"
)

// 组件名定义，对齐 ready 标准组件常量，对应就绪响应 checks 键名与链路日志。
const (
	ComponentDatabase = ready.ComponentDatabase
	ComponentCache    = ready.ComponentCache
	ComponentStorage  = ready.ComponentStorage
	ComponentSearch   = ready.ComponentSearch
	ComponentRealtime = ready.ComponentRealtime
	ComponentImgproxy = ready.ComponentImgproxy
	ComponentMailer   = ready.ComponentMailer
	ComponentTemporal = ready.ComponentTemporal
)

// Pinger 定义组件可达性探测接口。
type Pinger = ready.Pinger

// Deps 是探针所需的依赖。
type Deps struct {
	Database Pinger
	Cache    Pinger
	Storage  Pinger
	Search   Pinger
	Realtime Pinger
	Imgproxy Pinger
	Mailer   Pinger
	Temporal Pinger
}

// Service 协调调度底层 ready.Aggregator 评估实例能否接流量。
type Service struct {
	aggregator *ready.Aggregator
}

// buildCheck 统一为基础组件装配具备 Driver 与 Enabled 自动感知能力的探测规范。
// defaultDriver 传空串表示不指定默认驱动名（由探测实现自行声明）。
func buildCheck(name string, pinger Pinger, critical bool, defaultDriver string) ready.Check {
	opts := make([]ready.CheckOption, 0, 4)
	opts = append(opts, ready.WithCritical(critical))
	if defaultDriver != "" {
		opts = append(opts, ready.WithDriver(defaultDriver))
	}
	if da, ok := pinger.(interface{ Driver() string }); ok {
		opts = append(opts, ready.WithDriver(da.Driver()))
	}
	if ea, ok := pinger.(interface{ Enabled() bool }); ok {
		opts = append(opts, ready.WithEnabled(ea.Enabled))
	}
	return ready.NewCheck(name, ready.PingerFunc(pinger), opts...)
}

// NewService 构造探针服务，注册默认的核心与可选组件探针。
func NewService(deps Deps) *Service {
	agg := ready.New(
		buildCheck(ComponentDatabase, deps.Database, true /* critical */, "postgres"),
		buildCheck(ComponentCache, deps.Cache, false /* critical */, "valkey"),
	)

	optional := []struct {
		name          string
		pinger        Pinger
		defaultDriver string
	}{
		{ComponentStorage, deps.Storage, ""},
		{ComponentSearch, deps.Search, ""},
		{ComponentRealtime, deps.Realtime, ""},
		{ComponentImgproxy, deps.Imgproxy, ""},
		{ComponentMailer, deps.Mailer, ""},
		{ComponentTemporal, deps.Temporal, "temporal"},
	}
	for _, c := range optional {
		if c.pinger != nil {
			agg.Register(buildCheck(c.name, c.pinger, false /* critical */, c.defaultDriver))
		}
	}

	return &Service{aggregator: agg}
}

// Check 并发探测全部依赖的可达性状态，返回评估结果及失败错误字典。
func (s *Service) Check(ctx context.Context) (ready.Result, map[string]error) {
	return s.aggregator.Check(ctx)
}
