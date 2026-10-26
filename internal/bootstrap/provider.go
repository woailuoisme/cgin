package bootstrap

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/samber/do/v2"

	"cgin/internal/config"
	"cgin/pkg/eventbus"
)

// NewInjector 构造并组装系统全量依赖注入容器（单 RootScope）。
func NewInjector(options ...func(*do.InjectorOpts)) *do.RootScope {
	opts := &do.InjectorOpts{}
	for _, opt := range options {
		opt(opts)
	}

	return do.NewWithOpts(opts,
		InfraPackage,
		SubsystemPackage,
		SecurityPackage,
		ServicesPackage,
		HandlersPackage,
	)
}

// NewApplication 从 samber/do 容器启动并装配完整的 HTTP 应用实例。
// 启动期执行级联解析（Fail-Fast），任何初始化失败都将立即触发已构建服务的反向优雅清理。
func NewApplication(ctx context.Context, portOverride int) (*Application, error) {
	injector := NewInjector()

	if portOverride > 0 {
		cfg, err := do.Invoke[*config.Config](injector)
		if err != nil {
			_ = injector.ShutdownWithContext(ctx)
			return nil, err
		}
		cfg.App.Port = portOverride
	}

	// 触发全量拓扑解析（Fail-Fast）
	engine, err := do.Invoke[*gin.Engine](injector)
	if err != nil {
		_ = injector.ShutdownWithContext(ctx)
		return nil, err
	}

	cfg := do.MustInvoke[*config.Config](injector)
	log := do.MustInvoke[*zerolog.Logger](injector)
	bus := do.MustInvoke[*eventbus.Manager](injector)

	return &Application{
		cfg:      cfg,
		logger:   log,
		eventbus: bus,
		engine:   engine,
		injector: injector,
	}, nil
}
