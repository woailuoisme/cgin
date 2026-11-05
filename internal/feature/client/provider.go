package client

import (
	"github.com/samber/do/v2"

	"cgin/internal/config"
	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	clientauth "cgin/internal/feature/client/auth"
	authv1 "cgin/internal/feature/client/auth/v1"
	mediav1 "cgin/internal/feature/client/media/v1"
	clientpayment "cgin/internal/feature/client/payment"
	paymentv1 "cgin/internal/feature/client/payment/v1"
	clientprofile "cgin/internal/feature/client/profile"
	profilev1 "cgin/internal/feature/client/profile/v1"
	clientrealtime "cgin/internal/feature/client/realtime"
	realtimev1 "cgin/internal/feature/client/realtime/v1"
	"cgin/internal/user"
	platformauth "cgin/pkg/authentication"
	"cgin/pkg/cache"
	"cgin/pkg/eventbus"
	"cgin/pkg/i18n"
	"cgin/pkg/mailer"
	"cgin/pkg/media"
	"cgin/pkg/notification"
	"cgin/pkg/payment"
	platformRealtime "cgin/pkg/realtime"
	"cgin/pkg/storage"
)

// Package 声明 client 业务特性的 DI 依赖定义。
var Package = do.Package(
	// 1. 客户端认证服务与 HTTP Handler
	do.Lazy(func(i do.Injector) (*clientauth.AuthService, error) {
		db := do.MustInvoke[*database.DB](i)
		userService := do.MustInvoke[*user.Service](i)
		guard := do.MustInvokeNamed[*platformauth.Guard[*sqlc.User]](i, "client_guard")
		c := do.MustInvoke[cache.Store](i)
		mailerClient := do.MustInvoke[mailer.Mailer](i)
		notifier := do.MustInvoke[*notification.Notifier](i)
		translator := do.MustInvoke[i18n.Translator](i)
		cfg := do.MustInvoke[*config.Config](i)
		pub := do.MustInvoke[eventbus.Publisher](i)

		return clientauth.MustNewAuthService(clientauth.AuthDeps{
			DB:         db,
			Users:      userService,
			Guard:      guard,
			Cache:      c,
			Mailer:     mailerClient,
			Notifier:   notifier,
			Translator: translator,
			Config:     cfg,
			Publisher:  pub,
		}), nil
	}),

	do.Lazy(func(i do.Injector) (*authv1.Handler, error) {
		svc := do.MustInvoke[*clientauth.AuthService](i)
		return authv1.NewHandler(svc), nil
	}),

	// 2. 个人档案服务与 HTTP Handler
	do.Lazy(func(i do.Injector) (*clientprofile.ProfileService, error) {
		userService := do.MustInvoke[*user.Service](i)
		disk := do.MustInvoke[storage.Disk](i)
		return clientprofile.MustNewProfileService(clientprofile.ProfileDeps{
			Users:   userService,
			Storage: disk,
		}), nil
	}),

	do.Lazy(func(i do.Injector) (*profilev1.Handler, error) {
		svc := do.MustInvoke[*clientprofile.ProfileService](i)
		return profilev1.NewHandler(svc), nil
	}),

	// 3. 支付服务与 HTTP Handler
	do.Lazy(func(i do.Injector) (*clientpayment.PaymentService, error) {
		gateway := do.MustInvoke[payment.Gateway](i)
		c := do.MustInvoke[cache.Store](i)
		cfg := do.MustInvoke[*config.Config](i)
		pub := do.MustInvoke[eventbus.Publisher](i)

		return clientpayment.NewPaymentService(gateway, c, cfg, pub), nil
	}),

	do.Lazy(func(i do.Injector) (*paymentv1.Handler, error) {
		svc := do.MustInvoke[*clientpayment.PaymentService](i)
		return paymentv1.NewHandler(svc), nil
	}),

	// 4. 实时通信服务与 HTTP Handler
	do.Lazy(func(i do.Injector) (*clientrealtime.RealtimeService, error) {
		gateway := do.MustInvoke[platformRealtime.Gateway](i)
		cfg := do.MustInvoke[*config.Config](i)
		userService := do.MustInvoke[*user.Service](i)

		return clientrealtime.NewRealtimeService(clientrealtime.RealtimeDeps{
			Gateway: gateway,
			Cfg:     cfg,
			Secret:  cfg.Realtime.Secret,
			Users:   userService,
		}), nil
	}),

	do.Lazy(func(i do.Injector) (*realtimev1.Handler, error) {
		svc := do.MustInvoke[*clientrealtime.RealtimeService](i)
		return realtimev1.NewHandler(svc), nil
	}),

	// 5. 媒体处理 HTTP Handler
	do.Lazy(func(i do.Injector) (*mediav1.Handler, error) {
		uploader := do.MustInvoke[media.Uploader](i)
		return mediav1.NewHandler(uploader), nil
	}),

	// 6. 聚合 Handlers 供路由层统一装配
	do.Lazy(func(i do.Injector) (Handlers, error) {
		return Handlers{
			Auth:     do.MustInvoke[*authv1.Handler](i),
			Profile:  do.MustInvoke[*profilev1.Handler](i),
			Payment:  do.MustInvoke[*paymentv1.Handler](i),
			Media:    do.MustInvoke[*mediav1.Handler](i),
			Realtime: do.MustInvoke[*realtimev1.Handler](i),
		}, nil
	}),
)
