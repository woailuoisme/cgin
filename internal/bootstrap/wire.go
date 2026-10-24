package bootstrap

import (
	"context"
	"fmt"
	"slices"

	"github.com/dromara/carbon/v2"
	"github.com/uptrace/bun"
	"go.temporal.io/sdk/client"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"bgin/internal/config"
	"bgin/internal/database"
	adminauth "bgin/internal/features/admin/auth"
	admin "bgin/internal/features/admin/user"
	"bgin/internal/features/client/auth"
	"bgin/internal/features/client/profile"
	paymentFeature "bgin/internal/features/payment"
	realtimeFeature "bgin/internal/features/realtime"
	"bgin/internal/features/system"
	"bgin/internal/middleware"
	"bgin/internal/middleware/global"
	platformauth "bgin/internal/platform/authentication"
	"bgin/internal/platform/authorization"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/eventbus"
	"bgin/internal/platform/hasher"
	"bgin/internal/platform/i18n"
	"bgin/internal/platform/logger"
	"bgin/internal/platform/mailer"
	"bgin/internal/platform/notification"
	"bgin/internal/platform/payment"
	"bgin/internal/platform/pdf"
	"bgin/internal/platform/queue"
	"bgin/internal/platform/realtime"
	"bgin/internal/platform/search"
	platformsentry "bgin/internal/platform/sentry"
	"bgin/internal/platform/storage"
	"bgin/internal/platform/telemetry"
	"bgin/internal/platform/temporal"
	"bgin/internal/platform/wechat"
	"bgin/internal/shared/media"
	sharedDatabase "bgin/internal/shared/notification/database"
	"bgin/internal/shared/user"
)

// NewApplication 按依赖顺序完成装配：配置 → 日志 → 基础设施 → 领域 → HTTP。
func NewApplication(ctx context.Context, portOverride int) (*Application, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if portOverride > 0 {
		cfg.App.Port = portOverride
	}

	carbon.SetTimezone(cfg.App.Timezone)
	carbon.SetLocale(cfg.I18n.DefaultLanguage)

	// 登记 teardown 资源清理链，装配中途失败时逆序释放
	td := &teardown{}
	log, err := initLoggingAndSentry(ctx, cfg, td)
	if err != nil {
		return nil, err
	}

	// 初始化 PostgreSQL 数据库连接池
	db, err := database.Open(ctx, cfg.Database, cfg.App.Name)
	if err != nil {
		return nil, err
	}
	td.add(db.Close)

	logDatabaseReady(ctx, log, db, cfg.Database)

	cacheClient, locker, mailerClient, err := initInfraServices(ctx, cfg, log, td)
	if err != nil {
		return nil, td.fail(err)
	}

	eventBusManager, queueClient, err := initEventSubsystem(cfg, log, locker, td)
	if err != nil {
		return nil, td.fail(err)
	}

	// 探测并初始化文件/对象存储驱动
	disk, err := storage.Open(ctx, cfg.Storage)
	if err != nil {
		return nil, td.fail(err)
	}

	translator, err := i18n.New(cfg)
	if err != nil {
		return nil, td.fail(err)
	}
	i18n.SetDefault(translator)

	enforcer, err := authorization.New()
	if err != nil {
		return nil, td.fail(err)
	}

	tokens := platformauth.NewTokenIssuer(cfg.JWT, cacheClient)
	hasherClient := hasher.New()

	// 初始化用户认证提供器与用户领域服务
	userProvider := user.NewProvider(db, hasherClient)

	realtimeGateway, notifier, err := initNotificationSubsystem(cfg, db, mailerClient, queueClient, td)
	if err != nil {
		return nil, td.fail(err)
	}

	// 初始化客户端与管理端 Guard 及多守卫注册中心 (Multiple Guards)
	clientGuard := platformauth.NewGuard(tokens, userProvider, user.ClientPolicy{})
	adminGuard := platformauth.NewGuard(tokens, userProvider, user.AdminPolicy{})

	authManager := platformauth.NewManager().
		Register("client", clientGuard).
		Register("admin", adminGuard).
		SetDefault("client")

	globalMW := global.New(global.Deps{
		Cache:          cacheClient,
		Logger:         log,
		CORS:           cfg.CORS,
		Production:     cfg.IsProduction(),
		Translator:     translator,
		RequestTimeout: cfg.App.Server.RequestTimeout,
		// multipart 上传按媒体单文件配额放行；批量上传（UploadMultiple）受此单请求上限约束，
		// ponytail: 单请求上限 == 单文件配额，需要一次传多个大文件时再加 MaxFiles 配置。
		UploadMaxBody: cfg.Media.MaxFileSize,
	})

	routeMW := middleware.New(middleware.Deps{
		Tokens:      tokens,
		AuthManager: authManager,
		Cache:       cacheClient,
		Authz:       enforcer,
		Logger:      log,
		Limiter:     cfg.Limiter,
		SigningKey:  cfg.JWT.Secret,
	})

	authService := auth.NewService(auth.Deps{
		DB:         db,
		Guard:      clientGuard,
		Tokens:     tokens,
		Hasher:     hasherClient,
		Cache:      cacheClient,
		Mailer:     mailerClient,
		Notifier:   notifier,
		Translator: translator,
		Config:     cfg,
		Publisher:  eventBusManager,
	})

	userService := user.NewService(db)
	adminService := admin.NewService(admin.Deps{
		DB:          db,
		UserService: userService,
		Hasher:      hasherClient,
	})
	adminAuthService := adminauth.NewService(adminauth.Deps{
		DB:     db,
		Guard:  adminGuard,
		Tokens: tokens,
		Hasher: hasherClient,
	})

	searchClient, err := search.New(cfg.Search)
	if err != nil {
		return nil, td.fail(err)
	}

	mediaManager := media.New(cfg.Media, cfg.Storage, disk, db)

	// 初始化 Temporal 工作流客户端与就绪探针适配器
	temporalClient, temporalPinger, err := initTemporalSubsystem(cfg, log, td)
	if err != nil {
		return nil, td.fail(err)
	}

	// 初始化健康就绪探针 Handler
	systemHandler := system.NewHandler(
		system.NewService(system.Deps{
			Database: &databasePinger{db: db},
			Cache:    cacheClient,
			Storage:  disk,
			Search:   searchClient,
			Realtime: realtimeGateway,
			Imgproxy: mediaManager.Image(),
			Mailer:   mailerClient,
			Temporal: temporalPinger,
		}),
		log,
	)

	realtimeService := realtimeFeature.NewService(realtimeFeature.Deps{
		Gateway: realtimeGateway,
		Cfg:     cfg,
		Secret:  cfg.Realtime.Secret,
		DB:      db,
	})
	realtimeHandler := realtimeFeature.NewHandler(realtimeService)
	wechatManager, err := wechat.NewManager(cfg.WeChat, cacheClient)
	if err != nil {
		return nil, td.fail(err)
	}

	paymentManager, err := payment.NewManager(cfg, log)
	if err != nil {
		return nil, td.fail(err)
	}

	paymentService := paymentFeature.NewService(paymentManager, cacheClient, cfg, eventBusManager)
	paymentHandler := paymentFeature.NewHandler(paymentService)

	engine, err := buildEngine(
		cfg, globalMW, routeMW, systemHandler,
		auth.NewHandler(authService),
		profile.NewHandler(userService, mediaManager),
		admin.NewHandler(adminService),
		adminauth.NewHandler(adminAuthService),
		realtimeHandler,
		paymentHandler,
	)
	if err != nil {
		return nil, td.fail(err)
	}

	pdfService, err := pdf.RegisterPDF(cfg, disk, log)
	if err != nil {
		return nil, td.fail(err)
	}

	return &Application{
		cfg:          cfg,
		logger:       log,
		db:           db,
		cache:        cacheClient,
		locker:       locker,
		mailer:       mailerClient,
		notification: notifier,
		storage:      disk,
		media:        mediaManager,
		pdf:          pdfService,
		wechat:       wechatManager,
		payment:      paymentManager,
		realtime:     realtimeGateway,
		search:       searchClient,
		eventbus:     eventBusManager,
		queue:        queueClient,
		temporal:     temporalClient,
		engine:       engine,
	}, nil
}

// 编译期断言满足探针接口。
var (
	_ system.Pinger = (*databasePinger)(nil)
	_ system.Pinger = (*cache.Client)(nil)
	_ system.Pinger = (mailer.Mailer)(nil)
	_ system.Pinger = (*temporal.Pinger)(nil)
)

// databasePinger 将 *bun.DB 适配为 system.Pinger。
type databasePinger struct{ db *bun.DB }

// Ping 实现 system.Pinger。
func (p *databasePinger) Ping(ctx context.Context) error { return p.db.PingContext(ctx) }

// Driver 返回底层驱动标识。
func (*databasePinger) Driver() string { return "postgres" }

// logDatabaseReady 打印数据库连接池就绪日志并进行连接池配额诊断。
func logDatabaseReady(ctx context.Context, log *zap.Logger, db *bun.DB, cfg config.DatabaseConfig) {
	log.Info(
		"database connection pool ready",
		zap.Int("max_open_conns", cfg.MaxOpenConns),
		zap.Int("max_idle_conns", cfg.MaxIdleConns),
		zap.Duration("conn_max_lifetime", cfg.ConnMaxLifetime),
		zap.Duration("conn_max_idle_time", cfg.ConnMaxIdleTime),
		zap.Duration("connect_timeout", cfg.ConnectTimeout),
		zap.Duration("statement_timeout", cfg.StatementTimeout),
		zap.Duration("idle_in_tx_timeout", cfg.IdleInTxTimeout),
		zap.Duration("slow_threshold", cfg.SlowThreshold),
		zap.Bool("debug", cfg.Debug),
	)

	if warning, err := database.BudgetWarning(ctx, db, cfg.MaxOpenConns); err != nil {
		log.Debug("database connection budget check skipped", zap.Error(err))
	} else if warning != "" {
		log.Warn("database max open connections exceed configured budget", zap.String("detail", warning))
	}
}

// teardown 管理装配中途失败时的逆序资源清理。
type teardown struct {
	log     *zap.Logger
	closers []func() error
}

// add 登记资源释放回调函数。
func (t *teardown) add(closer func() error) { t.closers = append(t.closers, closer) }

// fail 逆序释放已登记资源并返回原始错误。
func (t *teardown) fail(cause error) error {
	for _, closer := range slices.Backward(t.closers) {
		if err := closer(); err != nil && t.log != nil {
			t.log.Warn("failed to close resource during teardown", zap.Error(err))
		}
	}
	return cause
}

// initLoggingAndSentry 初始化 OpenTelemetry 遥测、Sentry / GlitchTip 监控客户端与挂载 Sentry 桥接的核心日志器。
func initLoggingAndSentry(ctx context.Context, cfg *config.Config, td *teardown) (*zap.Logger, error) {
	telemetryShutdown, err := telemetry.Init(ctx, cfg.Telemetry, cfg.App.Name, cfg.App.Env, "")
	if err != nil {
		return nil, fmt.Errorf("init telemetry failed: %w", err)
	}
	td.add(func() error {
		return telemetryShutdown(context.Background())
	})

	sentryFlush, err := platformsentry.Init(cfg.Sentry, cfg.App.Name, cfg.App.Env, "")
	if err != nil {
		return nil, fmt.Errorf("init sentry client failed: %w", err)
	}

	var loggerOpts []logger.Option
	if cfg.Sentry.IsEnabled() {
		loggerOpts = append(loggerOpts, logger.WithExtraCore(platformsentry.NewZapCore(zapcore.WarnLevel)))
	}
	if core := telemetry.NewZapCore(cfg.Telemetry.EffectiveServiceName(cfg.App.Name), logger.ParseLevel(cfg.Log.Level)); core != nil {
		loggerOpts = append(loggerOpts, logger.WithExtraCore(core))
	}

	log := logger.New(cfg, loggerOpts...)
	td.log = log
	td.add(func() error {
		sentryFlush()
		return nil
	})

	return log, nil
}

// initInfraServices 初始化缓存、分布式锁与邮件客户端基础设施。
func initInfraServices(ctx context.Context, cfg *config.Config, log *zap.Logger, td *teardown) (*cache.Client, cache.Locker, mailer.Mailer, error) {
	cacheClient, err := cache.Open(ctx, cfg.Cache)
	if err != nil {
		return nil, nil, nil, err
	}
	td.add(cacheClient.Close)

	locker, err := cache.NewRedisLocker(cfg.Cache)
	if err != nil {
		return nil, nil, nil, err
	}
	td.add(locker.Close)

	mailerClient, err := mailer.New(cfg.Mail, log)
	if err != nil {
		return nil, nil, nil, err
	}
	td.add(mailerClient.Close)

	return cacheClient, locker, mailerClient, nil
}

// initEventSubsystem 装配异步任务队列客户端与基于 Watermill 的事件总线，并挂载领域事件监听。
func initEventSubsystem(cfg *config.Config, log *zap.Logger, locker cache.Locker, td *teardown) (*eventbus.Manager, *queue.Client, error) {
	queueClient, queueCleanup, err := queue.NewClient(cfg.Queue, queue.AdaptLocker(locker))
	if err != nil {
		return nil, nil, err
	}
	td.add(func() error {
		queueCleanup()
		return nil
	})

	eventBusManager, err := eventbus.NewManager(cfg.EventBus, log)
	if err != nil {
		return nil, nil, err
	}
	td.add(eventBusManager.Close)

	if err := auth.RegisterListeners(eventBusManager, queueClient); err != nil {
		return nil, nil, err
	}

	return eventBusManager, queueClient, nil
}

// initTemporalSubsystem 按需装配 Temporal 分布式工作流客户端与就绪探针适配器。
func initTemporalSubsystem(cfg *config.Config, log *zap.Logger, td *teardown) (client.Client, *temporal.Pinger, error) {
	if !cfg.Temporal.Enabled {
		return nil, temporal.NewPinger(nil, false), nil
	}
	temporalClient, err := temporal.NewClient(cfg.Temporal, log, cfg.Telemetry.Enabled)
	if err != nil {
		return nil, nil, err
	}
	td.add(func() error {
		temporalClient.Close()
		return nil
	})
	return temporalClient, temporal.NewPinger(temporalClient, true), nil
}

// initNotificationSubsystem 装配实时通知网关与统一通知引擎。
func initNotificationSubsystem(
	cfg *config.Config,
	db *bun.DB,
	mailerClient mailer.Mailer,
	queueClient *queue.Client,
	td *teardown,
) (realtime.Gateway, *notification.Notifier, error) {
	realtimeReg := realtime.NewRegistry()
	realtimeFeature.RegisterChannels(realtimeReg, db)

	realtimeGateway, err := realtime.RegisterRealtimeGateway(cfg, realtimeReg)
	if err != nil {
		return nil, nil, err
	}

	notifier, err := notification.New(cfg.Notification, mailerClient, realtimeGateway,
		sharedDatabase.NewChannel(db))
	if err != nil {
		return nil, nil, err
	}
	notifier.WithQueue(queueClient)
	td.add(notifier.Close)

	return realtimeGateway, notifier, nil
}
