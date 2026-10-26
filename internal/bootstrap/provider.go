package bootstrap

import (
	"context"
	"fmt"
	"slices"

	"github.com/dromara/carbon/v2"
	"github.com/rs/zerolog"

	"cgin/internal/config"
	"cgin/internal/database"
	admindto "cgin/internal/dto/admin"
	"cgin/internal/handler"
	adminhandler "cgin/internal/handler/admin"
	"cgin/internal/listener"
	"cgin/internal/media"
	"cgin/internal/middleware"
	"cgin/internal/middleware/global"
	"cgin/internal/notification/store"
	"cgin/internal/realtime"
	"cgin/internal/service"
	adminservice "cgin/internal/service/admin"
	"cgin/internal/user"
	platformauth "cgin/pkg/authentication"
	"cgin/pkg/authorization"
	"cgin/pkg/cache"
	"cgin/pkg/eventbus"
	"cgin/pkg/hasher"
	"cgin/pkg/i18n"
	"cgin/pkg/logger"
	"cgin/pkg/mailer"
	"cgin/pkg/notification"
	"cgin/pkg/payment"
	"cgin/pkg/queue"
	platformRealtime "cgin/pkg/realtime"
	"cgin/pkg/search"
	"cgin/pkg/storage"
	"cgin/pkg/telemetry"
	"cgin/pkg/validator"
)

// NewApplication 按依赖顺序完成装配：配置 → 日志 → 基础设施 → 领域 → HTTP。
//
// 装配中途失败时由 defer 逆序释放已登记资源，调用方无需逐处包装错误；
// 装配成功则把同一个关闭栈交给 Application，进程退出时复用，不再维护第二份清单。
func NewApplication(ctx context.Context, portOverride int) (_ *Application, err error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if portOverride > 0 {
		cfg.App.Port = portOverride
	}

	carbon.SetTimezone(cfg.App.Timezone)
	carbon.SetLocale(cfg.I18n.DefaultLanguage)

	resources := &lifecycle{}
	defer func() {
		if err != nil {
			resources.closeAll()
		}
	}()

	infra, err := initInfrastructure(ctx, cfg, resources)
	if err != nil {
		return nil, err
	}

	features, err := initFeatures(infra, resources)
	if err != nil {
		return nil, err
	}

	engine, err := buildEngine(cfg, features.global, features.route, features.handlers)
	if err != nil {
		return nil, err
	}

	return &Application{
		cfg:       cfg,
		logger:    infra.log,
		eventbus:  infra.eventbus,
		engine:    engine,
		resources: resources,
	}, nil
}

// infrastructure 汇总装配期构建的基础设施依赖。
//
// 字段只在这一层被赋值，功能层只读，避免组装过程变成一条几十个局部变量的长链。
type infrastructure struct {
	cfg        *config.Config
	log        *zerolog.Logger
	db         *database.DB
	cache      *cache.Client
	locker     cache.Locker
	mailer     mailer.Mailer
	eventbus   *eventbus.Manager
	queue      *queue.Client
	disk       storage.Disk
	translator *i18n.Translator
	validator  *validator.Validator
	enforcer   *authorization.Enforcer
	tokens     *platformauth.TokenIssuer
	hasher     *hasher.Hasher
}

// initInfrastructure 按依赖顺序构建配置、日志、连接池与平台组件。
func initInfrastructure(ctx context.Context, cfg *config.Config, lc *lifecycle) (*infrastructure, error) {
	log, err := initLogging(ctx, cfg, lc)
	if err != nil {
		return nil, err
	}

	pool, err := database.Open(ctx, cfg.Database, cfg.App.Name)
	if err != nil {
		return nil, err
	}
	db := database.NewDB(pool)
	lc.add("database connection", db.Close)
	logDatabaseReady(ctx, log, db, cfg.Database)

	services, err := initInfraServices(ctx, cfg, log, lc)
	if err != nil {
		return nil, err
	}

	events, err := initEventSubsystem(cfg, log, services.locker, lc)
	if err != nil {
		return nil, err
	}

	disk, err := storage.Open(ctx, cfg.Storage)
	if err != nil {
		return nil, err
	}

	translator, err := i18n.New(cfg)
	if err != nil {
		return nil, err
	}

	// 校验器在这里构造一次并挂载：规则由各领域模块显式贡献，不再靠 init() 自注册。
	val, err := validator.New(slices.Concat(user.ValidationOptions(), admindto.ValidationOptions())...)
	if err != nil {
		return nil, err
	}
	val.Install()

	enforcer, err := authorization.New()
	if err != nil {
		return nil, err
	}

	return &infrastructure{
		cfg:        cfg,
		log:        log,
		db:         db,
		cache:      services.cache,
		locker:     services.locker,
		mailer:     services.mailer,
		eventbus:   events.bus,
		queue:      events.queue,
		disk:       disk,
		translator: translator,
		validator:  val,
		enforcer:   enforcer,
		tokens:     platformauth.NewTokenIssuer(cfg.JWT, services.cache),
		hasher:     hasher.New(),
	}, nil
}

// featureSet 汇总领域装配产物：HTTP 处理器与两层中间件。
type featureSet struct {
	handlers routeHandlers
	global   *global.Middleware
	route    *middleware.Middleware
}

// initFeatures 装配用户域、认证守卫、通知网关与各垂直切片的 HTTP 处理器。
func initFeatures(in *infrastructure, lc *lifecycle) (*featureSet, error) {
	userProvider := user.MustNewProvider(in.db, in.hasher)
	userService := user.MustNewService(in.db, in.hasher)

	notifications, err := initNotificationSubsystem(in.cfg, in.db, in.mailer, in.queue, lc)
	if err != nil {
		return nil, err
	}

	clientGuard := platformauth.NewGuard(
		in.tokens,
		userProvider,
		user.ClientPolicy{RequireEmailVerification: in.cfg.Auth.RequireEmailVerification},
		userProvider.Identity,
	)
	adminGuard := platformauth.NewGuard(in.tokens, userProvider, user.AdminPolicy{}, userProvider.Identity)
	authManager := platformauth.NewManager().
		Register("client", clientGuard).
		Register("admin", adminGuard).
		SetDefault("client")

	globalMW := global.New(global.Deps{
		Cache:          in.cache,
		Logger:         in.log,
		CORS:           in.cfg.CORS,
		Production:     in.cfg.IsProduction(),
		Translator:     in.translator,
		Validator:      in.validator,
		RequestTimeout: in.cfg.App.Server.RequestTimeout,
		// multipart 上传按媒体单文件配额放行；批量上传（UploadMultiple）受此单请求上限约束，
		// ponytail: 单请求上限 == 单文件配额，需要一次传多个大文件时再加 MaxFiles 配置。
		UploadMaxBody: in.cfg.Media.MaxFileSize,
	})

	routeMW := middleware.New(middleware.Deps{
		AuthManager: authManager,
		Cache:       in.cache,
		Authz:       in.enforcer,
		Logger:      in.log,
		Limiter:     in.cfg.Limiter,
		SigningKey:  in.cfg.JWT.Secret,
	})

	authService := service.MustNewAuthService(service.AuthDeps{
		DB:          in.db,
		Users:       userService,
		Credentials: userProvider,
		Guard:       clientGuard,
		Hasher:      in.hasher,
		Cache:       in.cache,
		Mailer:      in.mailer,
		Notifier:    notifications.notifier,
		Translator:  in.translator,
		Config:      in.cfg,
		Publisher:   in.eventbus,
	})

	adminService := adminservice.MustNewUserService(adminservice.UserDeps{UserService: userService})
	adminAuthService := adminservice.MustNewAuthService(adminservice.AuthDeps{
		Guard:     adminGuard,
		Passwords: userProvider,
	})

	searchClient, err := search.New(in.cfg.Search)
	if err != nil {
		return nil, err
	}

	mediaManager := media.New(in.cfg.Media, in.cfg.Storage, in.disk, in.db)

	systemHandler := handler.NewSystemHandler(
		service.NewSystemService(service.SystemDeps{
			Database: &databasePinger{db: in.db},
			Cache:    in.cache,
			Storage:  in.disk,
			Search:   searchClient,
			Realtime: notifications.gateway,
			// 媒体门面自身实现 Ping/Driver/Enabled，探针直接依赖它而不需要伸手取内部件。
			Imgproxy: mediaManager,
			Mailer:   in.mailer,
		}),
		in.log,
	)

	realtimeService := service.NewRealtimeService(service.RealtimeDeps{
		Gateway: notifications.gateway,
		Cfg:     in.cfg,
		Secret:  in.cfg.Realtime.Secret,
		Users:   userService,
	})

	paymentManager, err := payment.NewManager(in.cfg, in.log)
	if err != nil {
		return nil, err
	}

	return &featureSet{
		handlers: routeHandlers{
			system:    systemHandler,
			auth:      handler.NewAuthHandler(authService),
			profile:   handler.NewProfileHandler(service.MustNewProfileService(service.ProfileDeps{Users: userService, Avatars: mediaManager})),
			admin:     adminhandler.NewUserHandler(adminService),
			adminAuth: adminhandler.NewAuthHandler(adminAuthService),
			realtime:  handler.NewRealtimeHandler(realtimeService),
			payment:   handler.NewPaymentHandler(service.NewPaymentService(paymentManager, in.cache, in.cfg, in.eventbus)),
		},
		global: globalMW,
		route:  routeMW,
	}, nil
}

// 编译期断言满足探针接口。
var (
	_ service.Pinger = (*databasePinger)(nil)
	_ service.Pinger = (*cache.Client)(nil)
	_ service.Pinger = mailer.Mailer(nil)
)

// databasePinger 将 *database.DB 适配为 service.Pinger。
type databasePinger struct{ db *database.DB }

// Ping 实现 service.Pinger。
func (p *databasePinger) Ping(ctx context.Context) error { return p.db.Ping(ctx) }

// Driver 返回底层驱动标识。
func (*databasePinger) Driver() string { return "postgres" }

// logDatabaseReady 打印数据库连接池就绪日志并进行连接池配额诊断。
func logDatabaseReady(ctx context.Context, log *zerolog.Logger, db *database.DB, cfg config.DatabaseConfig) {
	log.Info().
		Int("max_open_conns", cfg.MaxOpenConns).
		Int("max_idle_conns", cfg.MaxIdleConns).
		Dur("conn_max_lifetime", cfg.ConnMaxLifetime).
		Dur("conn_max_idle_time", cfg.ConnMaxIdleTime).
		Dur("connect_timeout", cfg.ConnectTimeout).
		Dur("statement_timeout", cfg.StatementTimeout).
		Dur("idle_in_tx_timeout", cfg.IdleInTxTimeout).
		Dur("slow_threshold", cfg.SlowThreshold).
		Bool("debug", cfg.Debug).
		Msg("database connection pool ready")

	if warning, err := database.BudgetWarning(ctx, db.Pool(), cfg.MaxOpenConns); err != nil {
		log.Debug().Err(err).Msg("database connection budget check skipped")
	} else if warning != "" {
		log.Warn().Str("detail", warning).Msg("database max open connections exceed configured budget")
	}
}

// initLogging 初始化 OpenTelemetry 遥测与挂载遥测桥接的核心日志器。
func initLogging(ctx context.Context, cfg *config.Config, lc *lifecycle) (*zerolog.Logger, error) {
	telemetryShutdown, err := telemetry.Init(ctx, cfg.Telemetry, cfg.App.Name, cfg.App.Env, "")
	if err != nil {
		return nil, fmt.Errorf("init telemetry failed: %w", err)
	}
	lc.add("telemetry providers", func() error {
		return telemetryShutdown(context.Background())
	})

	var loggerOpts []logger.Option
	if hook := telemetry.NewZerologHook(cfg.Telemetry.EffectiveServiceName(cfg.App.Name), logger.ParseLevel(cfg.Log.Level)); hook != nil {
		loggerOpts = append(loggerOpts, logger.WithHook(hook))
	}

	log := logger.New(cfg, loggerOpts...)
	lc.setLogger(log)
	return log, nil
}

// infraServices 汇总缓存客户端、分布式锁与邮件客户端。
type infraServices struct {
	cache  *cache.Client
	locker cache.Locker
	mailer mailer.Mailer
}

// initInfraServices 初始化缓存、分布式锁与邮件客户端基础设施。
//
// 分布式锁与缓存共用同一条连接，由 cache.Client.Close 内部一并释放，此处不重复登记。
func initInfraServices(ctx context.Context, cfg *config.Config, log *zerolog.Logger, lc *lifecycle) (*infraServices, error) {
	cacheClient, err := cache.Open(ctx, cfg.Cache)
	if err != nil {
		return nil, err
	}
	lc.add("cache client", cacheClient.Close)

	mailerClient, err := mailer.New(cfg.Mail, log)
	if err != nil {
		return nil, err
	}
	lc.add("smtp connection", mailerClient.Close)

	return &infraServices{
		cache:  cacheClient,
		locker: cacheClient.Locker(),
		mailer: mailerClient,
	}, nil
}

// eventSystem 汇总异步任务队列与领域事件总线。
type eventSystem struct {
	bus   *eventbus.Manager
	queue *queue.Client
}

// initEventSubsystem 装配异步任务队列与基于 Watermill 的事件总线，并挂载领域事件监听。
func initEventSubsystem(cfg *config.Config, log *zerolog.Logger, locker cache.Locker, lc *lifecycle) (*eventSystem, error) {
	queueClient, queueCleanup, err := queue.NewClient(cfg.Queue, queue.AdaptLocker(locker))
	if err != nil {
		return nil, err
	}
	lc.add("queue client", func() error {
		queueCleanup()
		return nil
	})

	bus, err := eventbus.NewManager(cfg.EventBus, log)
	if err != nil {
		return nil, err
	}
	lc.add("event bus manager", bus.Close)

	if err := listener.RegisterListeners(bus, queueClient); err != nil {
		return nil, err
	}

	return &eventSystem{bus: bus, queue: queueClient}, nil
}

// notificationSystem 汇总实时通知网关与统一通知引擎。
type notificationSystem struct {
	gateway  platformRealtime.Gateway
	notifier *notification.Notifier
}

// initNotificationSubsystem 装配实时通知网关与统一通知引擎。
func initNotificationSubsystem(
	cfg *config.Config,
	db *database.DB,
	mailerClient mailer.Mailer,
	queueClient *queue.Client,
	lc *lifecycle,
) (*notificationSystem, error) {
	realtimeReg := platformRealtime.NewRegistry()
	realtime.RegisterChannels(realtimeReg, db)

	gateway, err := platformRealtime.RegisterRealtimeGateway(cfg, realtimeReg)
	if err != nil {
		return nil, err
	}

	notifier, err := notification.New(cfg.Notification, mailerClient, gateway, store.NewChannel(db))
	if err != nil {
		return nil, err
	}
	notifier.WithQueue(queueClient)
	lc.add("notification worker pool", notifier.Close)

	return &notificationSystem{gateway: gateway, notifier: notifier}, nil
}
