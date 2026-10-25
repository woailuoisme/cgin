package seeders

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"

	"go.uber.org/zap"

	"cgin/internal/database"
	"cgin/internal/platform/logger"
)

// Seeder 是数据播种器必须实现的统一接口。
type Seeder interface {
	Seed(ctx context.Context, db *database.DB) error
}

// NamedSeeder 允许播种器显式声明自定义类名标识。
type NamedSeeder interface {
	Seeder
	Name() string
}

// StepHook 定义在每个 Seeder 开始与完成时的通知回调。
type StepHook func(name string, index, total int, duration time.Duration, done bool)

// SubProgressHook 定义在单个 Seeder 内部进行大批量数据生成时的记录进度回调。
type SubProgressHook func(current, total int64)

type (
	progressHookKey    struct{}
	subProgressHookKey struct{}
)

// WithProgressHook 将 Seeder 步骤回调存入上下文。
func WithProgressHook(ctx context.Context, hook StepHook) context.Context {
	return context.WithValue(ctx, progressHookKey{}, hook)
}

// ProgressHookFromContext 从上下文读取 Seeder 步骤回调。
func ProgressHookFromContext(ctx context.Context) StepHook {
	if hook, ok := ctx.Value(progressHookKey{}).(StepHook); ok {
		return hook
	}
	return nil
}

// WithSubProgressHook 将单个 Seeder 批量生成记录回调存入上下文。
func WithSubProgressHook(ctx context.Context, hook SubProgressHook) context.Context {
	return context.WithValue(ctx, subProgressHookKey{}, hook)
}

// SubProgressHookFromContext 从上下文读取单个 Seeder 批量生成记录回调。
func SubProgressHookFromContext(ctx context.Context) SubProgressHook {
	if hook, ok := ctx.Value(subProgressHookKey{}).(SubProgressHook); ok {
		return hook
	}
	return nil
}

// Runner 负责调度执行 Seeder 链条，并自动规避重复执行。
type Runner struct {
	db       *database.DB
	called   map[string]bool
	stepHook StepHook
	mu       sync.Mutex
}

// NewRunner 构造全新的播种调度器。
func NewRunner(db *database.DB) *Runner {
	return &Runner{
		db:     db,
		called: make(map[string]bool),
	}
}

// WithStepHook 为当前调度器显式绑定步骤回调钩子。
func (r *Runner) WithStepHook(hook StepHook) *Runner {
	r.stepHook = hook
	return r
}

// Call 依序调用并执行一组 Seeder。在同一 Runner 生命周期内，相同的 Seeder 仅会执行一次。
func (r *Runner) Call(ctx context.Context, seeders ...Seeder) error {
	hook := r.stepHook
	if hook == nil {
		hook = ProgressHookFromContext(ctx)
	}

	total := len(seeders)
	for i, s := range seeders {
		name := seederName(s)

		r.mu.Lock()
		if r.called[name] {
			r.mu.Unlock()
			logger.FromContext(ctx).Debug("seeder already executed, skipping duplicate seeding",
				zap.String("seeder", name))
			continue
		}
		// 标记当前 seeder 已在执行中，防止循环或重入
		r.called[name] = true
		r.mu.Unlock()

		if hook != nil {
			hook(name, i+1, total, 0, false)
		}

		start := time.Now()
		logger.FromContext(ctx).Info("executing database seeder...",
			zap.String("seeder", name))
		if err := s.Seed(ctx, r.db); err != nil {
			r.mu.Lock()
			delete(r.called, name)
			r.mu.Unlock()
			return fmt.Errorf("seeder [%s] execution failed: %w", name, err)
		}
		duration := time.Since(start)

		logger.FromContext(ctx).Info("database seeder completed",
			zap.String("seeder", name),
			zap.Duration("duration", duration))

		if hook != nil {
			hook(name, i+1, total, duration, true)
		}
	}
	return nil
}

// catalog 是内置播种器的只读索引；构造后不再变化。
//
// 不再依赖各文件 init() 自我注册：谁能被 db:seed 调用，在这一处就能读全。
var catalog = buildCatalog(
	&DatabaseSeeder{},
	&AdminSeeder{},
	&UserSeeder{},
)

// Get 按名称查找内置播种器。
func Get(name string) (Seeder, bool) {
	s, ok := catalog[name]
	return s, ok
}

// List 返回全部内置播种器名称（已排序）。
func List() []string {
	return slices.Sorted(maps.Keys(catalog))
}

// buildCatalog 按类名索引播种器；重名属于启动期编程错误，直接失败。
func buildCatalog(seeders ...Seeder) map[string]Seeder {
	index := make(map[string]Seeder, len(seeders))
	for _, s := range seeders {
		name := seederName(s)
		if _, exists := index[name]; exists {
			panic("seeders: duplicate seeder name: " + name)
		}
		index[name] = s
	}
	return index
}

// seederName 解析 Seeder 的类型名称。
func seederName(s Seeder) string {
	if named, ok := s.(NamedSeeder); ok && named.Name() != "" {
		return named.Name()
	}

	t := reflect.TypeOf(s)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}
