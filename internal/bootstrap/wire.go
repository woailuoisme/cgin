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
	"cgin/internal/middleware"
	"cgin/internal/middleware/global"
	"cgin/internal/service"
	adminservice "cgin/internal/service/admin"
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
	"cgin/pkg/realtime"
	"cgin/pkg/search"
	"cgin/pkg/storage"
	"cgin/pkg/telemetry"
	"cgin/pkg/validator"
	"cgin/shared/media"
	sharedDatabase "cgin/shared/notification/database"
	"cgin/shared/user"
)

// NewApplication 按依赖顺序完成装配：配置 → 日志 → 基础设施 → 领域 → HTTP。
//
// 装配中途失败时由 defer 逆序释放已登记资源，调用方无需逐处包装错误。
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

	// 登记 teardown 资源清理链，装配中途失败时逆序释放
	td := &teardown{}
	defer func() {
		if err != nil {
			td.closeAll()
		}
	}()

	infra, err := initInfrastructure(ctx, cfg, td)
	if err != nil {
		return nil, err
	}

	facade, err := initFeatures(infra, td)
	if err != nil {
		return nil, err
	}

	engine, err := buildEngine(cfg, facade.global, facade.route, facade.handlers)
	if err != nil {
		return nil, err
	}

	return &Application{
		cfg:          cfg,
		logger:       infra.log,
		db:           infra.db,
		cache:        infra.cache,
		locker:       infra.locker,
		mailer:       infra.mailer,
		notification: facade.notifier,
		eventbus:     infra.eventbus,
		queue:        infra.queue,
		engine:       engine,
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
func initInfrastructure(ctx context.Context, cfg *config.Config, td *teardown) (*infrastructure, error) {
	log, err := initLogging(ctx, cfg, td)
	if err != nil {
		return nil, err
	}

	pool, err := database.Open(ctx, cfg.Database, cfg.App.Name)
	if err != nil {
		return nil, err
	}
	db := database.NewDB(pool)
	td.add(db.Close)
	logDatabaseReady(ctx, log, db, cfg.Database)

	cacheClient, locker, mailerClient, err := initInfraServices(ctx, cfg, log, td)
	if err != nil {
		return nil, err
	}

	eventBusManager, queueClient, err := initEventSubsystem(cfg, log, locker, td)
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
		cache:      cacheClient,
		locker:     locker,
		mailer:     mailerClient,
		eventbus:   eventBusManager,
		queue:      queueClient,
		disk:       disk,
		translator: translator,
		validator:  val,
		enforcer:   enforcer,
		tokens:     platformauth.NewTokenIssuer(cfg.JWT, cacheClient),
		hasher:     hasher.New(),
	}, nil
}

// featureSet 汇总领域服务、HTTP 处理器与两层中间件。
type featureSet struct {
	handlers routeHandlers
	global   *global.Middleware
	route    *middleware.Middleware
	notifier *notification.Notifier
}

// initFeatures 装配用户域、认证守卫、通知网关与各垂直切片的 HTTP 处理器。
func initFeatures(in *infrastructure, td *teardown) (*featureSet, error) {
	userProvider := user.MustNewProvider(in.db, in.hasher)
	userService := user.MustNewService(in.db, in.hasher)

	realtimeGateway, notifier, err := initNotificationSubsystem(in.cfg, in.db, in.mailer, in.queue, td)
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

	authService := service.MustNewClientAuthService(service.ClientAuthDeps{
		DB:          in.db,
		Users:       userService,
		Credentials: userProvider,
		Guard:       clientGuard,
		Hasher:      in.hasher,
		Cache:       in.cache,
		Mailer:      in.mailer,
		Notifier:    notifier,
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
			Realtime: realtimeGateway,
			// 媒体门面自身实现 Ping/Driver/Enabled，探针直接依赖它而不需要伸手取内部件。
			Imgproxy: mediaManager,
			Mailer:   in.mailer,
		}),
		in.log,
	)

	realtimeService := service.NewRealtimeService(service.RealtimeDeps{
		Gateway: realtimeGateway,
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
			auth:      handler.NewClientAuthHandler(authService),
			profile:   handler.NewProfileHandler(service.MustNewProfileService(service.ProfileDeps{Users: userService, Avatars: mediaManager})),
			admin:     adminhandler.NewUserHandler(adminService),
			adminAuth: adminhandler.NewAuthHandler(adminAuthService),
			realtime:  handler.NewRealtimeHandler(realtimeService),
			payment:   handler.NewPaymentHandler(service.NewPaymentService(paymentManager, in.cache, in.cfg, in.eventbus)),
		},
		global:   globalMW,
		route:    routeMW,
		notifier: notifier,
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

// teardown 管理装配中途失败时的逆序资源清理。
type teardown struct {
	log     *zerolog.Logger
	closers []func() error
}

// add 登记资源释放回调函数。
func (t *teardown) add(closer func() error) { t.closers = append(t.closers, closer) }

// closeAll 逆序释放已登记资源；仅在装配失败时由 defer 调用。
func (t *teardown) closeAll() {
	for _, closer := range slices.Backward(t.closers) {
		if err := closer(); err != nil && t.log != nil {
			t.log.Warn().Err(err).Msg("failed to close resource during teardown")
		}
	}
}

// initLogging 初始化 OpenTelemetry 遥测与挂载遥测桥接的核心日志器。
func initLogging(ctx context.Context, cfg *config.Config, td *teardown) (*zerolog.Logger, error) {
	telemetryShutdown, err := telemetry.Init(ctx, cfg.Telemetry, cfg.App.Name, cfg.App.Env, "")
	if err != nil {
		return nil, fmt.Errorf("init telemetry failed: %w", err)
	}
	td.add(func() error {
		return telemetryShutdown(context.Background())
	})

	var loggerOpts []logger.Option
	if hook := telemetry.NewZerologHook(cfg.Telemetry.EffectiveServiceName(cfg.App.Name), logger.ParseLevel(cfg.Log.Level)); hook != nil {
		loggerOpts = append(loggerOpts, logger.WithHook(hook))
	}

	log := logger.New(cfg, loggerOpts...)
	td.log = log
	return log, nil
}

// initInfraServices 初始化缓存、分布式锁与邮件客户端基础设施。
func initInfraServices(ctx context.Context, cfg *config.Config, log *zerolog.Logger, td *teardown) (*cache.Client, cache.Locker, mailer.Mailer, error) {
	cacheClient, err := cache.Open(ctx, cfg.Cache)
	if err != nil {
		return nil, nil, nil, err
	}
	td.add(cacheClient.Close)

	mailerClient, err := mailer.New(cfg.Mail, log)
	if err != nil {
		return nil, nil, nil, err
	}
	td.add(mailerClient.Close)

	return cacheClient, cacheClient.Locker(), mailerClient, nil
}

// initEventSubsystem 装配异步任务队列客户端与基于 Watermill 的事件总线，并挂载领域事件监听。
func initEventSubsystem(cfg *config.Config, log *zerolog.Logger, locker cache.Locker, td *teardown) (*eventbus.Manager, *queue.Client, error) {
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

	if err := service.RegisterListeners(eventBusManager, queueClient); err != nil {
		return nil, nil, err
	}

	return eventBusManager, queueClient, nil
}

// initNotificationSubsystem 装配实时通知网关与统一通知引擎。
func initNotificationSubsystem(
	cfg *config.Config,
	db *database.DB,
	mailerClient mailer.Mailer,
	queueClient *queue.Client,
	td *teardown,
) (realtime.Gateway, *notification.Notifier, error) {
	realtimeReg := realtime.NewRegistry()
	service.RegisterChannels(realtimeReg, db)

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
