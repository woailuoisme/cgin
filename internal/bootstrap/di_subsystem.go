package bootstrap

import (
	"github.com/rs/zerolog"
	"github.com/samber/do/v2"

	"cgin/internal/config"
	"cgin/internal/database"
	"cgin/internal/dto"
	"cgin/internal/listener"
	"cgin/internal/notification/store"
	"cgin/internal/realtime"
	"cgin/pkg/cache"
	"cgin/pkg/eventbus"
	"cgin/pkg/mailer"
	"cgin/pkg/media"
	"cgin/pkg/notification"
	"cgin/pkg/payment"
	"cgin/pkg/queue"
	platformRealtime "cgin/pkg/realtime"
	"cgin/pkg/search"
	"cgin/pkg/storage"
)

// SubsystemPackage 声明异步任务、领域事件、实时网关、统一通知、媒体与收单等平台子系统。
var SubsystemPackage = do.Package(
	// 1. Asynq 异步任务队列客户端
	do.Lazy(func(i do.Injector) (*queue.Client, error) {
		cfg := do.MustInvoke[*config.Config](i)
		locker := do.MustInvoke[cache.Locker](i)
		log := do.MustInvoke[*zerolog.Logger](i)
		return queue.NewClient(cfg.Queue, queue.Options{
			Locker: queue.AdaptLocker(locker),
			Logger: log,
		})
	}),

	// 2. Watermill 领域事件总线与监听器自动注册
	do.Lazy(func(i do.Injector) (*eventbus.Manager, error) {
		cfg := do.MustInvoke[*config.Config](i)
		log := do.MustInvoke[*zerolog.Logger](i)
		queueClient := do.MustInvoke[*queue.Client](i)

		bus, err := eventbus.NewManager(cfg.EventBus, log)
		if err != nil {
			return nil, err
		}

		if err := listener.RegisterListeners(bus, queueClient); err != nil {
			_ = bus.Close()
			return nil, err
		}

		return bus, nil
	}),
	do.Lazy(func(i do.Injector) (eventbus.Publisher, error) {
		return do.MustInvoke[*eventbus.Manager](i), nil
	}),

	// 3. 实时通信频道注册表
	do.Lazy(func(i do.Injector) (*platformRealtime.Registry, error) {
		db := do.MustInvoke[*database.DB](i)
		reg := platformRealtime.NewRegistry()
		realtime.RegisterChannels(reg, db)
		return reg, nil
	}),

	// 4. Centrifugo 实时通信接入网关
	do.Lazy(func(i do.Injector) (platformRealtime.Gateway, error) {
		cfg := do.MustInvoke[*config.Config](i)
		reg := do.MustInvoke[*platformRealtime.Registry](i)
		return platformRealtime.NewManager(&cfg.Realtime, reg), nil
	}),

	// 5. 跨通道统一通知分发引擎
	do.Lazy(func(i do.Injector) (*notification.Notifier, error) {
		cfg := do.MustInvoke[*config.Config](i)
		db := do.MustInvoke[*database.DB](i)
		mailerClient := do.MustInvoke[mailer.Mailer](i)
		gateway := do.MustInvoke[platformRealtime.Gateway](i)
		queueClient := do.MustInvoke[*queue.Client](i)

		notifier, err := notification.New(cfg.Notification, mailerClient, gateway, store.NewChannel(db))
		if err != nil {
			return nil, err
		}
		notifier.WithQueue(queueClient)
		return notifier, nil
	}),

	// 6. 全文检索客户端
	do.Lazy(func(i do.Injector) (search.Client, error) {
		cfg := do.MustInvoke[*config.Config](i)
		return search.New(cfg.Search)
	}),

	// 7. 深度媒体处理引擎
	do.Lazy(func(i do.Injector) (media.Engine, error) {
		cfg := do.MustInvoke[*config.Config](i)
		disk := do.MustInvoke[storage.Disk](i)
		engine := media.NewEngine(cfg.Media, cfg.Storage, disk)
		media.SetDefaultEngine(engine)
		dto.SetDefaultEngine(engine)
		return engine, nil
	}),

	// 8. 多渠道支付收单管理器
	do.Lazy(func(i do.Injector) (*payment.Manager, error) {
		cfg := do.MustInvoke[*config.Config](i)
		log := do.MustInvoke[*zerolog.Logger](i)
		return payment.NewManager(cfg, log)
	}),
)
