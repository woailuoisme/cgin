package bootstrap

import (
	"github.com/samber/do/v2"

	"cgin/internal/config"
	"cgin/internal/database"
	clientauth "cgin/internal/feature/client/auth"
	clientauthstore "cgin/internal/feature/client/auth/store"
	clientrealtime "cgin/internal/feature/client/realtime"
	"cgin/pkg/cache"
	"cgin/pkg/eventbus"
	"cgin/pkg/logger"
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
	// 1. Asynq 任务多路复用路由器与业务任务挂载
	do.Lazy(func(i do.Injector) (*queue.TaskRouter, error) {
		router := queue.NewTaskRouter()
		mailerClient := do.MustInvoke[mailer.Mailer](i)
		clientauth.RegisterJobs(router, mailerClient)
		return router, nil
	}),

	// 2. Asynq 任务生产者客户端 (API 服务仅依赖此轻量组件)
	do.Lazy(func(i do.Injector) (queue.Producer, error) {
		cfg := do.MustInvoke[*config.Config](i)
		log := do.MustInvoke[*logger.Logger](i)
		return queue.NewProducer(queue.Config{
			RedisURL: cfg.Queue.RedisURL,
		}, queue.WithProducerLogger(log))
	}),

	// 3. Asynq 任务消费端 Server (Worker 守护进程专职运行)
	do.Lazy(func(i do.Injector) (*queue.Server, error) {
		cfg := do.MustInvoke[*config.Config](i)
		locker := do.MustInvoke[cache.Locker](i)
		log := do.MustInvoke[*logger.Logger](i)
		return queue.NewServer(queue.Config{
			RedisURL:        cfg.Queue.RedisURL,
			Concurrency:     cfg.Queue.Concurrency,
			ShutdownTimeout: cfg.Queue.ShutdownTimeout,
		},
			queue.WithServerLogger(log),
			queue.WithServerLocker(queue.AdaptLocker(locker)),
		)
	}),

	// 4. Asynq 运维巡检探针 (CLI/后台运维专属)
	do.Lazy(func(i do.Injector) (queue.Inspector, error) {
		cfg := do.MustInvoke[*config.Config](i)
		log := do.MustInvoke[*logger.Logger](i)
		return queue.NewInspector(queue.Config{
			RedisURL: cfg.Queue.RedisURL,
		}, queue.WithInspectorLogger(log))
	}),

	// 5. Watermill 领域事件总线与监听器自动注册
	do.Lazy(func(i do.Injector) (*eventbus.Manager, error) {
		cfg := do.MustInvoke[*config.Config](i)
		log := do.MustInvoke[*logger.Logger](i)
		producer := do.MustInvoke[queue.Producer](i)

		bus, err := eventbus.NewManager(cfg.EventBus, log)
		if err != nil {
			return nil, err
		}

		if err := clientauth.RegisterListeners(bus, producer); err != nil {
			_ = bus.Close()
			return nil, err
		}

		return bus, nil
	}),
	do.Lazy(func(i do.Injector) (eventbus.Publisher, error) {
		return do.MustInvoke[*eventbus.Manager](i), nil
	}),

	// 6. 实时通信频道注册表
	do.Lazy(func(i do.Injector) (*platformRealtime.Registry, error) {
		db := do.MustInvoke[*database.DB](i)
		reg := platformRealtime.NewRegistry()
		clientrealtime.RegisterChannels(reg, db)
		return reg, nil
	}),

	// 7. Centrifugo 实时通信接入网关
	do.Lazy(func(i do.Injector) (platformRealtime.Gateway, error) {
		cfg := do.MustInvoke[*config.Config](i)
		reg := do.MustInvoke[*platformRealtime.Registry](i)
		return platformRealtime.NewManager(&cfg.Realtime, reg)
	}),

	// 8. 跨通道统一通知分发引擎
	do.Lazy(func(i do.Injector) (*notification.Notifier, error) {
		cfg := do.MustInvoke[*config.Config](i)
		db := do.MustInvoke[*database.DB](i)
		mailerClient := do.MustInvoke[mailer.Mailer](i)
		gateway := do.MustInvoke[platformRealtime.Gateway](i)
		producer := do.MustInvoke[queue.Producer](i)

		notifier, err := notification.New(cfg.Notification, mailerClient, gateway, clientauthstore.NewChannel(db))
		if err != nil {
			return nil, err
		}
		notifier.WithQueue(producer)
		return notifier, nil
	}),

	// 6. 全文检索客户端
	do.Lazy(func(i do.Injector) (search.Client, error) {
		cfg := do.MustInvoke[*config.Config](i)
		return search.New(cfg.Search)
	}),

	// 9. 富媒体防篡改签名与变体计算器 (纯内存/HMAC-SHA256)
	do.Lazy(func(i do.Injector) (media.Signer, error) {
		cfg := do.MustInvoke[*config.Config](i)
		return media.NewSigner(media.SignerConfig{
			Imgproxy: media.ImgproxyConfig{
				BaseURL:        cfg.Media.Imgproxy.BaseURL,
				InternalURL:    cfg.Media.Imgproxy.InternalURL,
				Key:            cfg.Media.Imgproxy.Key,
				Salt:           cfg.Media.Imgproxy.Salt,
				Insecure:       cfg.Media.Imgproxy.Insecure,
				DefaultFormat:  cfg.Media.Imgproxy.DefaultFormat,
				DefaultQuality: cfg.Media.Imgproxy.DefaultQuality,
			},
			StorageType: cfg.Storage.Driver,
			S3Bucket:    cfg.Storage.S3.Bucket,
		}), nil
	}),

	// 10. 预签名文件直传协调器
	do.Lazy(func(i do.Injector) (media.Uploader, error) {
		disk := do.MustInvoke[storage.Disk](i)
		presigner, _ := storage.AsPresigner(disk)
		return media.NewUploader(presigner), nil
	}),

	// 8. 多渠道支付收单管理器
	do.Lazy(func(i do.Injector) (*payment.Manager, error) {
		cfg := do.MustInvoke[*config.Config](i)
		log := do.MustInvoke[*logger.Logger](i)
		return payment.NewManager(cfg.Payment, log)
	}),
	do.Lazy(func(i do.Injector) (payment.Gateway, error) {
		return do.MustInvoke[*payment.Manager](i), nil
	}),
)
