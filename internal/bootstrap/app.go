// Package bootstrap 负责应用依赖装配与生命周期管理。
package bootstrap

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/samber/do/v2"

	"cgin/internal/config"
	"cgin/pkg/eventbus"
)

// Application 聚合装配完成的 HTTP 引擎与依赖容器。
type Application struct {
	cfg      *config.Config
	logger   *zerolog.Logger
	eventbus *eventbus.Manager
	engine   *gin.Engine
	injector *do.RootScope
}

// Engine 暴露已装配的路由引擎，供集成测试直接驱动请求。
func (a *Application) Engine() *gin.Engine { return a.engine }

// Injector 暴露根依赖注入容器，供集成测试克隆或内省。
func (a *Application) Injector() *do.RootScope { return a.injector }

// Close 逆序释放由 samber/do 拓扑管理登记的全部外部资源。
func (a *Application) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.injector.ShutdownWithContext(ctx); err != nil && a.logger != nil {
		a.logger.Error().Err(err).Msg("application shutdown did not complete cleanly")
	}
}
