// Package bootstrap 负责应用依赖装配与生命周期管理。
package bootstrap

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"cgin/internal/config"
	"cgin/internal/database"
	"cgin/pkg/cache"
	"cgin/pkg/eventbus"
	"cgin/pkg/mailer"
	"cgin/pkg/notification"
	"cgin/pkg/queue"
	"cgin/pkg/telemetry"
)

// Application 聚合装配完成的系统依赖与 HTTP 引擎。
//
// 只保留需要被读取或参与关闭的依赖；纯构造中间产物（媒体、PDF、支付等）
// 已由各自的调用方持有，不需要在此额外挂一个只写不读的字段。
type Application struct {
	cfg          *config.Config
	logger       *zerolog.Logger
	db           *database.DB
	cache        *cache.Client
	locker       cache.Locker
	mailer       mailer.Mailer
	notification *notification.Notifier
	eventbus     *eventbus.Manager
	queue        *queue.Client
	engine       *gin.Engine
}

// Engine 暴露已装配的路由引擎，供集成测试直接驱动请求。
func (a *Application) Engine() *gin.Engine { return a.engine }

// Locker 暴露系统级分布式互斥锁管理器（支持看门狗自动续期与原子释放）。
func (a *Application) Locker() cache.Locker { return a.locker }

// EventBus 暴露已装配的领域事件总线管理器。
func (a *Application) EventBus() *eventbus.Manager { return a.eventbus }

// Queue 暴露已装配的异步队列客户端。
func (a *Application) Queue() *queue.Client { return a.queue }

// Close 释放全部外部资源与客户端连接。
func (a *Application) Close() {
	if a.eventbus != nil {
		a.closeResource("event bus manager", a.eventbus.Close)
	}
	if a.queue != nil {
		a.closeResource("queue client", a.queue.Close)
	}
	if a.notification != nil {
		a.closeResource("notification worker pool", a.notification.Close)
	}
	if a.mailer != nil {
		a.closeResource("SMTP connection", a.mailer.Close)
	}
	if a.locker != nil {
		a.closeResource("distributed locker", a.locker.Close)
	}
	if a.cache != nil {
		a.closeResource("Redis connection", a.cache.Close)
	}
	if a.db != nil {
		a.closeResource("database connection", a.db.Close)
	}
	if err := telemetry.Shutdown(context.Background()); err != nil {
		a.logger.Warn().Err(err).Msg("failed to shutdown telemetry")
	}
}

func (a *Application) closeResource(name string, closeFn func() error) {
	if err := closeFn(); err != nil {
		a.logger.Warn().Err(err).Msg("failed to close " + name)
	}
}
