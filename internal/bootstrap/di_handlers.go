package bootstrap

import (
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/samber/do/v2"

	"cgin/internal/config"
	"cgin/internal/handler"
	adminhandler "cgin/internal/handler/admin"
	"cgin/internal/middleware"
	"cgin/internal/middleware/global"
	"cgin/internal/service"
	adminservice "cgin/internal/service/admin"
	"cgin/pkg/media"
)

// HandlersPackage 声明 HTTP 传输适配层（各模块 Handler、路由表与 Gin 路由引擎）。
var HandlersPackage = do.Package(
	// 1. 系统探针处理器
	do.Lazy(func(i do.Injector) (*handler.SystemHandler, error) {
		svc := do.MustInvoke[*service.SystemService](i)
		log := do.MustInvoke[*zerolog.Logger](i)
		return handler.NewSystemHandler(svc, log), nil
	}),

	// 2. 客户端认证处理器
	do.Lazy(func(i do.Injector) (*handler.AuthHandler, error) {
		svc := do.MustInvoke[*service.AuthService](i)
		return handler.NewAuthHandler(svc), nil
	}),

	// 3. 个人资料处理器
	do.Lazy(func(i do.Injector) (*handler.ProfileHandler, error) {
		svc := do.MustInvoke[*service.ProfileService](i)
		return handler.NewProfileHandler(svc), nil
	}),

	// 4. 管理端用户管理处理器
	do.Lazy(func(i do.Injector) (*adminhandler.UserHandler, error) {
		svc := do.MustInvoke[*adminservice.UserService](i)
		return adminhandler.NewUserHandler(svc), nil
	}),

	// 5. 管理端认证处理器
	do.Lazy(func(i do.Injector) (*adminhandler.AuthHandler, error) {
		svc := do.MustInvoke[*adminservice.AuthService](i)
		return adminhandler.NewAuthHandler(svc), nil
	}),

	// 6. 实时通信处理器
	do.Lazy(func(i do.Injector) (*handler.RealtimeHandler, error) {
		svc := do.MustInvoke[*service.RealtimeService](i)
		return handler.NewRealtimeHandler(svc), nil
	}),

	// 7. 收单支付处理器
	do.Lazy(func(i do.Injector) (*handler.PaymentHandler, error) {
		svc := do.MustInvoke[*service.PaymentService](i)
		return handler.NewPaymentHandler(svc), nil
	}),

	// 8. 媒体资源处理器
	do.Lazy(func(i do.Injector) (*handler.MediaHandler, error) {
		engine := do.MustInvoke[media.Engine](i)
		return handler.NewMediaHandler(engine), nil
	}),

	// 9. 路由处理器聚合
	do.Lazy(func(i do.Injector) (routeHandlers, error) {
		return routeHandlers{
			system:    do.MustInvoke[*handler.SystemHandler](i),
			auth:      do.MustInvoke[*handler.AuthHandler](i),
			profile:   do.MustInvoke[*handler.ProfileHandler](i),
			media:     do.MustInvoke[*handler.MediaHandler](i),
			admin:     do.MustInvoke[*adminhandler.UserHandler](i),
			adminAuth: do.MustInvoke[*adminhandler.AuthHandler](i),
			realtime:  do.MustInvoke[*handler.RealtimeHandler](i),
			payment:   do.MustInvoke[*handler.PaymentHandler](i),
		}, nil
	}),

	// 10. Gin HTTP 引擎与洋葱中间件管线
	do.Lazy(func(i do.Injector) (*gin.Engine, error) {
		cfg := do.MustInvoke[*config.Config](i)
		globalMW := do.MustInvoke[*global.Middleware](i)
		routeMW := do.MustInvoke[*middleware.Middleware](i)
		handlers := do.MustInvoke[routeHandlers](i)

		return buildEngine(cfg, globalMW, routeMW, handlers)
	}),
)
