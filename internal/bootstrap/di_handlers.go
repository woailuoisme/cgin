package bootstrap

import (
	"github.com/gin-gonic/gin"
	"github.com/samber/do/v2"

	"cgin/internal/config"
	"cgin/internal/feature/admin"
	"cgin/internal/feature/client"
	"cgin/internal/feature/system"
	"cgin/internal/middleware"
	"cgin/internal/middleware/global"
)

// HandlersPackage 声明 HTTP 传输适配层（路由处理器聚合与 Gin 路由引擎）。
var HandlersPackage = do.Package(
	// 1. 路由处理器聚合
	do.Lazy(func(i do.Injector) (routeHandlers, error) {
		return routeHandlers{
			system: do.MustInvoke[*system.SystemHandler](i),
			client: do.MustInvoke[client.Handlers](i),
			admin:  do.MustInvoke[admin.Handlers](i),
		}, nil
	}),

	// 2. Gin HTTP 引擎与洋葱中间件管线
	do.Lazy(func(i do.Injector) (*gin.Engine, error) {
		cfg := do.MustInvoke[*config.Config](i)
		globalMW := do.MustInvoke[*global.Middleware](i)
		routeMW := do.MustInvoke[*middleware.Middleware](i)
		handlers := do.MustInvoke[routeHandlers](i)

		return buildEngine(cfg, globalMW, routeMW, handlers)
	}),
)
