# 0007. Gin 声明式查询绑定、路由分组与存储适配器深化

## 上下文与决策

为进一步消除代码库中的浅层模块（Shallow Modules）、重复中间件参数传递以及流式资源生命周期泄漏，依据 Gin 官方推荐工程实践，对 Web 引擎管道及存储接缝进行以下 4 项深化改造：

1. **声明式查询参数绑定与浅层解析模块消除 (Declarative Query Binding & Elimination of Manual Parsers)**：
   在 `internal/features/admin/user/dto.go` 的 `ListUsersQuery` 上声明 `form` 结构体标签与 `validate` 校验标签，由 Gin 原生 `c.ShouldBindQuery(&query)` 直接接管查询参数解析、类型转换与分页约束校验。彻底删除 `admin.Handler` 中手写的 `parseListQuery` 与 `optionalInt` 浅层辅助函数（通过 Deletion Test 消除 33 行冗余代码）。并在 `internal/platform/errs/render.go` 中识别 `*strconv.NumError`，保证非法数字参数统一按 `400 Bad Request` 优雅拒绝。
2. **语义化路由分组与级联中间件子路由 (Semantic Route Grouping & Cascaded Middleware Sub-Routers)**：
   重构 `internal/features/auth/routes.go`，利用 Gin 原生 `group.Group("", ...)` 构建语义化子路由分支（敏感配额子路由与鉴权保护子路由），消除在 12 个端点注册时反复传递 `sensitive` 与 `authenticated, protected` 冗余中间件参数。中间件策略在子路由接缝处一次性集中声明，新增端点自动继承。
3. **Multipart 上传流与对象存储适配器深化 (Multipart Upload & Storage Disk Adapter)**：
   在 `internal/platform/storage/storage.go` 的 `storage.Disk` 核心接缝上增加 `PutMultipart(ctx context.Context, path string, file *multipart.FileHeader) error` 接口方法。将 `file.Open()`、流消费与 `defer src.Close()` 保底资源释放完全封装在 `localDisk` 与 `s3Disk` 底层适配器内部，调用方仅需单行调用即可安全落盘或上传至 S3。
4. **多道请求体检查与上下文缓存绑定 (Multi-Pass Body Inspection via Cached Body Binding)**：
   在 `internal/middleware/security.go` 中引入 `CacheBody` 中间件，支持与 Gin 原生 `c.ShouldBindBodyWith(obj, binding.JSON)` 接缝机制协同，将请求体持久化到上下文 `gin.BodyBytesKey`，彻底规避 `io.Reader` 单次排空导致的 `EOF` 异常，为上游安全审计/验签与下游业务反序列化提供安全的多道并发读取能力。
