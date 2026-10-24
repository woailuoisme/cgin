# 0009. 语义化权限矩阵解耦与 Context 驱动请求截止期深化

## 上下文与决策

针对 Gin 社区常见切面中间件（`gin-contrib/authz`、`gin-contrib/timeout`、`gin-contrib/secure` 与 `gin-contrib/graceful`），依据代码库设计原则（模块深度 Depth、架构接缝 Seam、局部性 Locality 与删除测试），明确以下 4 项核心架构契约：

1. **语义化权限接缝拒绝 URL 路径耦合 (Semantic RBAC Seam over URL Coupling)**：
   明确拒绝引入 `gin-contrib/authz`。该中间件将 Casbin 策略硬编码耦合到 HTTP 路径与动词（`e.Enforce(sub, path, method)`），不仅引发脆弱的正则匹配且破坏统一错误信封。`bgin` 坚守 `middleware.Can(resource, action)` 语义化接缝，由内嵌编译的 Casbin v3 引擎判定权限，并通过 `_ = c.Error(errs.Forbidden(...))` 由后置 `ErrorHandler` 统一渲染规范的 `api.md` 5.2 JSON 错误响应。
2. **Context 驱动请求截止期接缝 (Context-Driven Request Deadline Seam)**：
   明确拒绝引入 `gin-contrib/timeout`。该库在子协程中运行 Handler 并用计时器竞争，导致下游 Goroutine 在超时后依然并发写入非线程安全的 `gin.Context`，存在严重的数据竞态缺陷。`bgin` 在引擎接缝中引入纯 Context 驱动的 `mw.Timeout()` 中间件，直接向 `c.Request.Context()` 注入 `context.WithTimeout`（默认 15s）。下游 Bun/pgx 与 Valkey 的 I/O 在超时时主动取消退出，由后置 `ErrorHandler` 统一将 `context.DeadlineExceeded` 转化为 RFC 9110 标准的 `504 GATEWAY_TIMEOUT` 响应。
3. **保持零依赖极简安全响应头局部性 (Lean Security Headers with High Locality)**：
   明确拒绝引入 `gin-contrib/secure`。该库具备 30+ 冗余配置参数，且其默认激进的 Content Security Policy (CSP) 会直接破坏 `/docs` 上的 Scalar 在线文档渲染。`bgin` 维持现有的 15 行零依赖 `SecurityHeaders` 中间件，通过删除测试验证其高局部性与极简性，精准下发 `nosniff`、`DENY`、`no-referrer` 并在生产环境条件激活 HSTS。
4. **标准库优雅停机与全栈资源回收 (Stdlib Graceful Shutdown and Infrastructure Drain)**：
   明确拒绝引入 `gin-contrib/graceful`。该库仅为标准库 `http.Server.Shutdown(ctx)` 的浅层包装，Gin 官方亦明确推荐直用标准库。`bgin` 在 `internal/bootstrap/server.go` 与 `internal/console/serve.go` 中统领应用生命周期，在收到停机信号后按序排空 HTTP 监听、数据库活动事务连接与 Valkey 缓存客户端。
