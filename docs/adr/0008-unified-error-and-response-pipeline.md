# 0008. 全局统一错误与成功响应管道深化 (Unified Error and Response Pipeline)

## 上下文与决策

为彻底杜绝框架级原生纯文本输出（如未匹配路由或方法时的 `404 page not found` 与 `405 method not allowed`），确保 100% 的 HTTP 响应严格满足 [api.md](file:///Users/seaside/Projects/go/gin-bun/api.md) 5.1（成功响应与分页信封）与 5.2（全局统一错误模型）契约规范，决定对 Web 引擎管道及响应接缝进行以下 4 项深化改造：

1. **引擎级 NoRoute 统一错误接缝 (Global NoRoute Seam)**：
   在 `internal/bootstrap/engine.go` 中注册 `engine.NoRoute` 回退处理函数，统一向当前请求上下文派发 `c.Error(errs.NotFound(""))` 并中止后续链条，交由后置 `middleware.ErrorHandler` 解析并渲染统一的 `{ "code": "NOT_FOUND", "message": "..." }` 扁平 JSON 结构，彻底根除框架纯文本泄漏。
2. **严格 MethodNotAllowed 协商与 Allow 响应头下发 (RFC-Compliant 405 Seam)**：
   在引擎中显式开启 `engine.HandleMethodNotAllowed = true`，并挂载 `engine.NoMethod` 回退处理函数。当客户端使用不受支持的 HTTP 动词访问已注册的端点时，自动携带 RFC 规范的 `Allow` 响应头（如 `Allow: GET, PATCH, OPTIONS`），并返回统一的 `{ "code": "METHOD_NOT_ALLOWED", "message": "..." }` 结构化 JSON 响应。
3. **成功与分页信封非空集合保证 (Null-Safe Collection & Pagination Envelopes)**：
   在 `internal/platform/response/response.go` 抽象层中强化数组不变量：`response.Paginated` 确保空集合恒定输出 `items: []` 而绝非 `items: null`；新增 `response.Collection` 函数保证无信封集合的非空序列化安全，规避前端遍历崩溃。
4. **错误元数据与 AccessLog 结构化观测接缝协同 (Coordinated Error Context & AccessLog)**：
   在 `middleware.ErrorHandler` 渲染非 2xx 响应时，将格式化后的业务错误码（`resp.Code`）回写至请求上下文（`c.Set("error_code", ...)`）；`middleware.AccessLog` 提取该标识并合并进同一条结构化访问日志中，消灭分布式追踪中错误日志与访问日志割裂的痛点。
