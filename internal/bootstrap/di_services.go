package bootstrap

import (
	"context"

	"github.com/samber/do/v2"

	"cgin/internal/config"
	"cgin/internal/database"
	"cgin/internal/media"
	"cgin/internal/service"
	adminservice "cgin/internal/service/admin"
	"cgin/internal/user"
	"cgin/pkg/cache"
	"cgin/pkg/eventbus"
	"cgin/pkg/hasher"
	"cgin/pkg/i18n"
	"cgin/pkg/mailer"
	"cgin/pkg/notification"
	"cgin/pkg/payment"
	platformRealtime "cgin/pkg/realtime"
	"cgin/pkg/search"
	"cgin/pkg/storage"
)

// databasePinger 将 *database.DB 适配为 service.Pinger。
type databasePinger struct{ db *database.DB }

func (p *databasePinger) Ping(ctx context.Context) error { return p.db.Ping(ctx) }
func (*databasePinger) Driver() string                   { return "postgres" }

// ServicesPackage 声明系统全部领域服务与用例协调器。
var ServicesPackage = do.Package(
	// 1. 认证核心用例服务
	do.Lazy(func(i do.Injector) (*service.AuthService, error) {
		db := do.MustInvoke[*database.DB](i)
		userService := do.MustInvoke[*user.Service](i)
		userProvider := do.MustInvoke[*user.Provider](i)
		guards := do.MustInvoke[AuthGuards](i)
		h := do.MustInvoke[hasher.PasswordHasher](i)
		c := do.MustInvoke[cache.Store](i)
		mailerClient := do.MustInvoke[mailer.Mailer](i)
		notifier := do.MustInvoke[*notification.Notifier](i)
		translator := do.MustInvoke[i18n.Translator](i)
		cfg := do.MustInvoke[*config.Config](i)
		pub := do.MustInvoke[eventbus.Publisher](i)

		return service.MustNewAuthService(service.AuthDeps{
			DB:          db,
			Users:       userService,
			Credentials: userProvider,
			Guard:       guards.Client,
			Hasher:      h,
			Cache:       c,
			Mailer:      mailerClient,
			Notifier:    notifier,
			Translator:  translator,
			Config:      cfg,
			Publisher:   pub,
		}), nil
	}),

	// 2. 管理端用户管理服务
	do.Lazy(func(i do.Injector) (*adminservice.UserService, error) {
		userService := do.MustInvoke[*user.Service](i)
		return adminservice.MustNewUserService(adminservice.UserDeps{UserService: userService}), nil
	}),

	// 3. 管理端认证服务
	do.Lazy(func(i do.Injector) (*adminservice.AuthService, error) {
		guards := do.MustInvoke[AuthGuards](i)
		userProvider := do.MustInvoke[*user.Provider](i)
		return adminservice.MustNewAuthService(adminservice.AuthDeps{
			Guard:     guards.Admin,
			Passwords: userProvider,
		}), nil
	}),

	// 4. 系统探针与指标诊断服务
	do.Lazy(func(i do.Injector) (*service.SystemService, error) {
		db := do.MustInvoke[*database.DB](i)
		c := do.MustInvoke[cache.Store](i)
		disk := do.MustInvoke[storage.Disk](i)
		searchClient := do.MustInvoke[search.Client](i)
		gateway := do.MustInvoke[platformRealtime.Gateway](i)
		mediaManager := do.MustInvoke[*media.Manager](i)
		mailerClient := do.MustInvoke[mailer.Mailer](i)

		return service.NewSystemService(service.SystemDeps{
			Database: &databasePinger{db: db},
			Cache:    c,
			Storage:  disk,
			Search:   searchClient,
			Realtime: gateway,
			Imgproxy: mediaManager,
			Mailer:   mailerClient,
		}), nil
	}),

	// 5. 实时通信凭据与频道分发服务
	do.Lazy(func(i do.Injector) (*service.RealtimeService, error) {
		gateway := do.MustInvoke[platformRealtime.Gateway](i)
		cfg := do.MustInvoke[*config.Config](i)
		userService := do.MustInvoke[*user.Service](i)

		return service.NewRealtimeService(service.RealtimeDeps{
			Gateway: gateway,
			Cfg:     cfg,
			Secret:  cfg.Realtime.Secret,
			Users:   userService,
		}), nil
	}),

	// 6. 个人资料维护服务
	do.Lazy(func(i do.Injector) (*service.ProfileService, error) {
		userService := do.MustInvoke[*user.Service](i)
		mediaManager := do.MustInvoke[*media.Manager](i)
		return service.MustNewProfileService(service.ProfileDeps{
			Users:   userService,
			Avatars: mediaManager,
		}), nil
	}),

	// 7. 收单与支付状态机服务
	do.Lazy(func(i do.Injector) (*service.PaymentService, error) {
		pm := do.MustInvoke[*payment.Manager](i)
		c := do.MustInvoke[cache.Store](i)
		cfg := do.MustInvoke[*config.Config](i)
		pub := do.MustInvoke[eventbus.Publisher](i)
		return service.NewPaymentService(pm, c, cfg, pub), nil
	}),
)
