package bootstrap

import (
	"errors"
	"fmt"
	"io"

	"github.com/gin-gonic/gin"

	"bgin/internal/config"
	adminauth "bgin/internal/features/admin/auth"
	admin "bgin/internal/features/admin/user"
	"bgin/internal/features/client/auth"
	"bgin/internal/features/client/profile"
	paymentFeature "bgin/internal/features/payment"
	realtimeFeature "bgin/internal/features/realtime"
	"bgin/internal/features/system"
	"bgin/internal/middleware"
	"bgin/internal/middleware/global"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/validator"
)

// buildEngine 组装 gin 引擎与全部路由。
func buildEngine(
	cfg *config.Config,
	globalMW *global.Middleware,
	routeMW *middleware.Middleware,
	systemHandler *system.Handler,
	authHandler *auth.Handler,
	profileHandler *profile.Handler,
	adminHandler *admin.Handler,
	adminAuthHandler *adminauth.Handler,
	realtimeHandler *realtimeFeature.Handler,
	paymentHandler *paymentFeature.Handler,
) (*gin.Engine, error) {
	if err := validator.InitEngine(); err != nil {
		return nil, fmt.Errorf("init validator engine failed: %w", err)
	}

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

	// 全局中间件链：按洋葱模型分层编排（链路观测 -> 协议安全 -> 异常治理 -> 载荷控制）
	engine.Use(
		globalMW.Telemetry(),       // 1. 链路追踪与指标：OpenTelemetry 根 Span 创建与服务度量采集
		globalMW.RequestID(),       // 2. 请求唯一标识：提取或生成 X-Request-ID 并注入上下文日志器
		globalMW.AccessLog(),       // 3. 访问日志：记录请求方法、路径、状态码、IP 与端到端耗时
		globalMW.CORS(),            // 4. 跨域策略：注入跨域标头并对 OPTIONS 预检请求快速短路
		globalMW.SecurityHeaders(), // 5. 浏览器安全头：注入 nosniff、DENY、no-referrer 与 HSTS 等标头
		globalMW.ErrorHandler(),    // 6. 错误处理信封：集中捕获 c.Errors 并翻译渲染统一 JSON 响应
		globalMW.Recovery(),        // 7. 恐慌兜底恢复：捕获未处理 panic 记录堆栈并转为 500 错误
		globalMW.Sentry(),          // 8. 异常监控上报：挂载 Sentry 请求 Hub 并捕获 panic 与 5xx 异常
		globalMW.Timeout(),         // 9. 超时控制：请求生命周期截止期控制，超时抛出 504（长连接豁免）
		globalMW.BodyLimit(),       // 10. 请求体限额：限制请求体大小（普通 1MiB / 上传 32MiB），防大包攻击
		globalMW.CacheBody(),       // 11. 请求体缓存：预读普通请求体至 BodyBytesKey，避免下游读取 EOF
		globalMW.Idempotency(),     // 12. 接口幂等控制：基于 Idempotency-Key 进行并发锁定与成功响应回放
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
	system.RegisterRoutes(engine, systemHandler)

	// 仅在开启 ENABLE_DOCS 时注册 API 文档路由
	if cfg.App.EnableDocs {
		registerDocs(engine)
	}

	v1 := engine.Group("/v1")
	auth.RegisterRoutes(v1, authHandler, routeMW)
	profile.RegisterRoutes(v1, profileHandler, routeMW)
	adminauth.RegisterAdminAuthRoutes(v1, adminAuthHandler, routeMW)
	admin.RegisterAdminRoutes(v1, adminHandler, routeMW)
	realtimeFeature.RegisterRoutes(v1, realtimeHandler, routeMW)
	paymentFeature.RegisterRoutes(v1, paymentHandler, routeMW)

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

	return buildEngine(
		cfg,
		globalMW,
		routeMW,
		&system.Handler{},
		&auth.Handler{},
		&profile.Handler{},
		&admin.Handler{},
		&adminauth.Handler{},
		&realtimeFeature.Handler{},
		&paymentFeature.Handler{},
	)
}
