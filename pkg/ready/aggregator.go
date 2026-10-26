package ready

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/alexliesenfeld/health"
	"github.com/samber/lo"
)

// DefaultCheckTimeout 是单个组件探针的默认超时上限。
const DefaultCheckTimeout = 1 * time.Second

// DefaultCacheDuration 是就绪探针防雪崩缓存的默认生命周期。
const DefaultCacheDuration = 1 * time.Second

// Aggregator 负责并发调度已注册的就绪探针并聚合整体评估结果。
// 底层基于 alexliesenfeld/health 工业级健康检查引擎，具备防雪崩缓存与异步探针调度能力。
type Aggregator struct {
	mu             sync.RWMutex
	checks         []Check
	defaultTimeout time.Duration
	cacheDuration  time.Duration
	checker        health.Checker
	dirty          bool
	latencies      sync.Map // key: string (check.Name), value: time.Duration
}

// New 创建就绪探针聚合器实例。
func New(checks ...Check) *Aggregator {
	return &Aggregator{
		checks:         slices.Clone(checks),
		defaultTimeout: DefaultCheckTimeout,
		cacheDuration:  DefaultCacheDuration,
		dirty:          true,
	}
}

// SetDefaultTimeout 配置单项检查默认超时上限。
func (a *Aggregator) SetDefaultTimeout(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if d > 0 {
		a.defaultTimeout = d
		a.dirty = true
	}
}

// SetCacheDuration 配置探针防雪崩缓存有效期。若为 0 则禁用缓存（每次实时穿透探测）。
func (a *Aggregator) SetCacheDuration(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cacheDuration = d
	a.dirty = true
}

// Register 显式注册一个或多个待测组件规范。
func (a *Aggregator) Register(checks ...Check) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checks = append(a.checks, checks...)
	a.dirty = true
}

// rebuildCheckerLocked 构建底层 health.Checker 实例。调用方必须持有 a.mu。
func (a *Aggregator) rebuildCheckerLocked() {
	opts := []health.CheckerOption{
		health.WithDisabledAutostart(),
	}

	if a.defaultTimeout > 0 {
		opts = append(opts, health.WithTimeout(a.defaultTimeout))
	}

	opts = append(opts, lo.Ternary(a.cacheDuration > 0, health.WithCacheDuration(a.cacheDuration), health.WithDisabledCache()))

	for _, c := range a.checks {
		check := c
		timeout := cmp.Or(check.Timeout, a.defaultTimeout)

		opts = append(opts, health.WithCheck(health.Check{
			Name:    check.Name,
			Timeout: timeout,
			Check: func(ctx context.Context) (err error) {
				if !check.IsEnabled() {
					return nil
				}
				if check.Ping == nil {
					return ErrProbeNotConfigured
				}

				start := time.Now()
				defer func() {
					elapsed := time.Since(start)
					a.latencies.Store(check.Name, elapsed)

					if r := recover(); r != nil {
						err = fmt.Errorf("probe panic: %v", r)
					}
				}()

				return check.Ping(ctx)
			},
		}))
	}

	a.checker = health.NewChecker(opts...)
	a.dirty = false
}

// Check 并发探测所有已注册组件，返回聚合评估结果及失败错误字典（供日志记录）。
func (a *Aggregator) Check(ctx context.Context) (Result, map[string]error) {
	ctx = cmp.Or(ctx, context.Background())

	a.mu.Lock()
	if a.checker == nil || a.dirty {
		a.rebuildCheckerLocked()
	}
	checker := a.checker
	checks := slices.Clone(a.checks)
	a.mu.Unlock()

	checkerResult := checker.Check(ctx)

	reducer := newResultReducer()
	errs := make(map[string]error)

	for _, check := range checks {
		if !check.IsEnabled() {
			reducer.addDisabled(check)
			continue
		}

		var checkErr error
		if detail, ok := checkerResult.Details[check.Name]; ok {
			checkErr = detail.Error
		}

		var latency time.Duration
		if v, ok := a.latencies.Load(check.Name); ok {
			if d, ok := v.(time.Duration); ok {
				latency = d
			}
		}

		reducer.addChecked(check, checkErr, latency)
		if checkErr != nil {
			errs[check.Name] = checkErr
		}
	}

	return reducer.snapshot(), errs
}
