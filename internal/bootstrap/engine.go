package bootstrap

import (
	"errors"
	"fmt"
	"io"

	"github.com/gin-gonic/gin"

	"cgin/internal/config"
	"cgin/internal/feature/admin"
	authadminv1 "cgin/internal/feature/admin/auth/v1"
	useradminv1 "cgin/internal/feature/admin/user/v1"
	"cgin/internal/feature/client"
	authclientv1 "cgin/internal/feature/client/auth/v1"
	mediaclientv1 "cgin/internal/feature/client/media/v1"
	paymentclientv1 "cgin/internal/feature/client/payment/v1"
	profileclientv1 "cgin/internal/feature/client/profile/v1"
	realtimeclientv1 "cgin/internal/feature/client/realtime/v1"
	"cgin/internal/feature/system"
	"cgin/internal/middleware"
	"cgin/internal/middleware/global"
	"cgin/pkg/errs"
)

// routeHandlers 汇总各模块的 HTTP 处理器，由装配方填充。
type routeHandlers struct {
	system *system.SystemHandler
	client client.Handlers
	admin  admin.Handlers
}

// mount 把全部模块挂载到 /v1 分组下。
func (h routeHandlers) mount(v1 *gin.RouterGroup, mw *middleware.Middleware) {
	client.RegisterRoutes(v1, h.client, mw)
	admin.RegisterRoutes(v1, h.admin, mw)
}

// zeroHandlers 提供仅用于撑起路由表的零值处理器。
// 探测只登记端点不执行处理器，但指针须非 nil 且类型正确，因为 RegisterRoutes 会保存它们。
func zeroHandlers() routeHandlers {
	return routeHandlers{
		system: &system.SystemHandler{},
		client: client.Handlers{
			Auth:     &authclientv1.Handler{},
			Profile:  &profileclientv1.Handler{},
			Media:    &mediaclientv1.Handler{},
			Payment:  &paymentclientv1.Handler{},
			Realtime: &realtimeclientv1.Handler{},
		},
		admin: admin.Handlers{
			Auth: &authadminv1.Handler{},
			User: &useradminv1.Handler{},
		},
	}
}

// buildEngine 组装 gin 引擎与全部路由。
func buildEngine(
	cfg *config.Config,
	globalMW *global.Middleware,
	routeMW *middleware.Middleware,
	handlers routeHandlers,
) (*gin.Engine, error) {
	// 设置 Gin 运行模式并静默默认控制台输出，由结构化日志中间件统一记录
	gin.SetMode(cfg.ResolveGinMode())
	gin.DefaultWriter = io.Discard
	gin.DebugPrintRouteFunc = func(httpMethod, absolutePath, handlerName string, numHandlers int) {}

	engine := gin.New()
	engine.HandleMethodNotAllowed = true

	// 不信任代理头，客户端真实 IP 直接取 RemoteAddr，防止 IP 伪造
	if err := engine.SetTrustedProxies(nil); err != nil {
		return nil, fmt.Errorf("set trusted proxies failed: %w", err)
	}

	// 全局中间件链按洋葱模型分层，顺序即依赖：观测要在最外层包裹，信封与恢复要能兜住后续链路。
	engine.Use(
		// 静态资源与浏览器降噪
		globalMW.Favicon(),

		// 链路观测
		globalMW.Telemetry(),
		globalMW.RequestID(),
		globalMW.AccessLog(),

		// 协议安全
		globalMW.CORS(),
		globalMW.I18n(),

		// 异常治理与超时控制（ErrorHandler 在 Timeout 内部执行，确保错误状态码写入 Timeout 缓冲）
		globalMW.Recovery(),
		globalMW.Timeout(),
		globalMW.ErrorHandler(),

		// 载荷控制
		globalMW.BodyLimit(),
		globalMW.EnforceJSONContentType(),
		globalMW.Idempotency(),
	)

	// 统一 404 与 405 错误响应，交由统一错误处理中间件渲染
	abortWithError := func(err error) gin.HandlerFunc {
		return func(c *gin.Context) {
			_ = c.Error(err)
			c.Abort()
		}
	}
	engine.NoRoute(abortWithError(errs.NotFound("")))
	engine.NoMethod(abortWithError(errs.MethodNotAllowed("")))

	// 注册根路径与系统探针路由（/、/livez、/readyz）
	system.RegisterRoutes(engine, handlers.system)

	// 仅在开启 ENABLE_DOCS 时注册 API 文档路由
	if cfg.App.EnableDocs {
		registerDocs(engine)
	}

	v1 := engine.Group("/v1")
	handlers.mount(v1, routeMW)

	return engine, nil
}

// InspectEngine 组装用于路由探测与文档自省的轻量级 Gin 引擎实例（零数据库与缓存网络连接）。
func InspectEngine(cfg *config.Config) (*gin.Engine, error) {
	if cfg == nil {
		return nil, errors.New("bootstrap: inspect engine requires a non-nil config")
	}

	globalMW := global.New(global.Deps{
		CORS:           cfg.CORS,
		Production:     cfg.IsProduction(),
		RequestTimeout: cfg.App.Server.RequestTimeout,
	})

	routeMW := middleware.New(middleware.Deps{
		Limiter: cfg.Limiter,
	})

	return buildEngine(cfg, globalMW, routeMW, zeroHandlers())
}
