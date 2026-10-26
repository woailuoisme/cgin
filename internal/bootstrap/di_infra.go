package bootstrap

import (
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/dromara/carbon/v2"
	"github.com/rs/zerolog"
	"github.com/samber/do/v2"

	"cgin/internal/config"
	"cgin/internal/database"
	admindto "cgin/internal/dto/admin"
	"cgin/internal/user"
	"cgin/pkg/authorization"
	"cgin/pkg/cache"
	"cgin/pkg/geoip"
	"cgin/pkg/hasher"
	"cgin/pkg/i18n"
	"cgin/pkg/logger"
	"cgin/pkg/mailer"
	"cgin/pkg/storage"
	"cgin/pkg/telemetry"
	"cgin/pkg/validator"
)

// telemetryResource 包装遥测优雅停机回调，满足 do.ShutdownerWithContextAndError 契约。
type telemetryResource struct {
	shutdown func(context.Context) error
}

func (t *telemetryResource) Shutdown(ctx context.Context) error {
	if t != nil && t.shutdown != nil {
		return t.shutdown(ctx)
	}
	return nil
}

// InfraPackage 声明系统基础设施层核心组件（配置、日志、连接池、缓存、邮件、存储等）。
var InfraPackage = do.Package(
	// 1. 强类型环境配置
	do.Lazy(func(i do.Injector) (*config.Config, error) {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		carbon.SetTimezone(cfg.App.Timezone)
		carbon.SetLocale(cfg.I18n.DefaultLanguage)
		return cfg, nil
	}),

	// 2. 遥测提供商
	do.Lazy(func(i do.Injector) (*telemetryResource, error) {
		cfg := do.MustInvoke[*config.Config](i)
		shutdown, err := telemetry.Init(context.Background(), cfg.Telemetry, cfg.App.Name, cfg.App.Env, "")
		if err != nil {
			return nil, fmt.Errorf("init telemetry failed: %w", err)
		}
		return &telemetryResource{shutdown: shutdown}, nil
	}),

	// 3. Zerolog 结构化日志记录器
	do.Lazy(func(i do.Injector) (*zerolog.Logger, error) {
		cfg := do.MustInvoke[*config.Config](i)
		_ = do.MustInvoke[*telemetryResource](i)

		var loggerOpts []logger.Option
		if hook := telemetry.NewZerologHook(cfg.Telemetry.EffectiveServiceName(cfg.App.Name), logger.ParseLevel(cfg.Log.Level)); hook != nil {
			loggerOpts = append(loggerOpts, logger.WithHook(hook))
		}

		return logger.New(cfg, loggerOpts...), nil
	}),

	// 4. PostgreSQL 数据连接入口与连接池
	do.Lazy(func(i do.Injector) (*database.DB, error) {
		cfg := do.MustInvoke[*config.Config](i)
		log := do.MustInvoke[*zerolog.Logger](i)
		ctx := context.Background()

		// 旧名已改语义（database/sql 的空闲上限 → pgxpool 的常驻下限），
		// 遗留环境变量会被静默忽略，启动期显式告警一次。
		if os.Getenv("DB_MAX_IDLE_CONNS") != "" {
			log.Warn().
				Str("deprecated", "DB_MAX_IDLE_CONNS").
				Str("replacement", "DB_MIN_CONNS").
				Msg("legacy database pool variable is ignored, renamed to DB_MIN_CONNS")
		}

		pool, err := database.Open(ctx, cfg.Database, cfg.App.Name)
		if err != nil {
			return nil, err
		}
		db := database.NewDB(pool)
		logDatabaseReady(ctx, log, db, cfg.Database)
		return db, nil
	}),

	// 5. Valkey / Redis 缓存客户端与统一 Store 接口绑定
	do.Lazy(func(i do.Injector) (*cache.Client, error) {
		cfg := do.MustInvoke[*config.Config](i)
		return cache.Open(context.Background(), cfg.Cache)
	}),
	do.Lazy(func(i do.Injector) (cache.Store, error) {
		return do.MustInvoke[*cache.Client](i), nil
	}),

	// 6. 分布式锁管理器（优先从 cache.Store 提取 LockProvider，支持测试无缝切换）
	do.Lazy(func(i do.Injector) (cache.Locker, error) {
		store := do.MustInvoke[cache.Store](i)
		if lp, ok := store.(cache.LockProvider); ok && lp.Locker() != nil {
			return lp.Locker(), nil
		}
		return cache.NewMemoryLocker(), nil
	}),

	// 7. 邮件发送客户端（原生满足 do.ShutdownerWithError 优雅停机契约）
	do.Lazy(func(i do.Injector) (mailer.Mailer, error) {
		cfg := do.MustInvoke[*config.Config](i)
		log := do.MustInvoke[*zerolog.Logger](i)

		return mailer.New(cfg.Mail, log)
	}),

	// 8. 对象存储抽象
	do.Lazy(func(i do.Injector) (storage.Disk, error) {
		cfg := do.MustInvoke[*config.Config](i)
		return storage.Open(context.Background(), cfg.Storage)
	}),

	// 9. 多语言翻译门面
	do.Lazy(func(i do.Injector) (i18n.Translator, error) {
		cfg := do.MustInvoke[*config.Config](i)
		return i18n.New(cfg)
	}),

	// 10. 国际化参数校验器
	do.Lazy(func(i do.Injector) (*validator.Validator, error) {
		val, err := validator.New(slices.Concat(user.ValidationOptions(), admindto.ValidationOptions())...)
		if err != nil {
			return nil, err
		}
		val.Install()
		return val, nil
	}),

	// 11. 密码哈希器（统一绑定 PasswordHasher 契约）
	do.Lazy(func(i do.Injector) (hasher.PasswordHasher, error) {
		return hasher.New(), nil
	}),

	// 12. Casbin 权限决策引擎
	do.Lazy(func(i do.Injector) (*authorization.Enforcer, error) {
		return authorization.New()
	}),

	// 13. IP 归属地服务（内置优雅停机与降级）
	do.Lazy(func(i do.Injector) (geoip.Service, error) {
		cfg := do.MustInvoke[*config.Config](i)
		log := do.MustInvoke[*zerolog.Logger](i)
		return geoip.NewService(cfg.GeoIP, log)
	}),
)

// logDatabaseReady 打印数据库连接池就绪日志并进行连接池配额诊断。
// 只保留决定容量与阻塞行为的配置：池上下限、连接寿命、语句超时与 SQL 调试开关。
func logDatabaseReady(ctx context.Context, log *zerolog.Logger, db *database.DB, cfg config.DatabaseConfig) {
	log.Info().
		Int("max_open_conns", cfg.MaxOpenConns).
		Int("min_conns", cfg.MinConns).
		Str("conn_max_lifetime", cfg.ConnMaxLifetime.String()).
		Str("statement_timeout", cfg.StatementTimeout.String()).
		Bool("debug", cfg.Debug).
		Msg("database connection pool ready")

	if warning, err := database.BudgetWarning(ctx, db.Pool(), cfg.MaxOpenConns); err != nil {
		log.Debug().Err(err).Msg("database connection budget check skipped")
	} else if warning != "" {
		log.Warn().Str("detail", warning).Msg("database max open connections exceed configured budget")
	}
}
