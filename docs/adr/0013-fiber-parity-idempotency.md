# ADR-0013: 幂等中间件对齐 Fiber 官方实现

## 背景

原幂等中间件是自研实现：以业务指纹比对载荷、用缓存记录 `processing/completed` 状态、并发冲突返回 409、仅缓存 2xx。它与 Fiber 官方 `middleware/idempotency` 的语义存在四处偏差：键校验规则不同、并发模型不同、响应缓存范围不同、缺少可注入的存储与锁抽象。

## 决策

在 `internal/middleware/global/idempotency` 下按 Fiber 的文件与类型结构重建，Gin 仅替换传输层调用：

1. **配置同构**：`Config` 暴露 `Lock`、`Storage`、`Next`、`KeyHeaderValidate`、`KeyScope`、`KeyHeader`、`KeepResponseHeaders`、`Lifetime`、`DisableValueRedaction`，默认值集中在 `ConfigDefault`。`KeyHeader` 默认沿用本仓库与 `api.md` 已承诺的 IETF `Idempotency-Key`（Fiber 默认 `X-Idempotency-Key`）。
2. **并发模型同构**：无锁读缓存 → 按键加锁 → 锁内二次读 → 执行处理器 → 写缓存。相同键的并发请求串行化，后来者回放先完成者的响应，不再返回 409。
3. **响应回放同构**：缓存状态码、响应头与响应体；默认排除 `Set-Cookie`/`Set-Cookie2`，`KeepResponseHeaders` 非 nil 时只回放白名单；读取与写入两侧使用同一过滤器。
4. **失败语义同构**：处理器通过 `c.Error` 表达失败（等价于 Fiber 的 `c.Next()` 返回 error），失败请求不写缓存；缓存读取、序列化、加锁失败统一返回 500。
5. **作用域同构**：`KeyScope` 与键经长度前缀后取 SHA-256，输出定长 64 位十六进制；作用域为空时直接使用原始键。
6. **可注入后端**：默认 `MemoryLock` + `MemoryStorage` 与 Fiber 一致；全局装配注入 `cache.Store` 适配器，使响应回放与互斥跨实例共享。`Locker`/`Storage` 方法接收 `context.Context`，以遵守项目的 Context 贯穿约束并支持请求取消。
7. **可观测性**：保留 `IsFromCache` / `WasPutToCache`；日志中的幂等键默认脱敏。

## 契约变化

- `Idempotency-Key` 默认必须是 36 字符 UUID；`X-Idempotency-Key` 兼容头与强制模式（`Enforce`）移除。
- 相同键不再因载荷不同返回 422，也不再因并发处理中返回 409；`IDEMPOTENCY_REQUIRED`、`IDEMPOTENCY_CONFLICT`、`IDEMPOTENCY_MISMATCH` 从错误码契约中删除。
- 显式写出的非 2xx 响应同样会被缓存（Fiber 语义）；只有 `c.Error` 失败路径不缓存。

## 后果

- 正向：幂等语义与业界参考实现一致，存储与锁可替换，跨实例行为明确，回放保真度覆盖状态码与多值响应头。
- 负向：客户端必须使用 36 字符键；失败响应若由处理器显式写出而非 `c.Error`，仍会被缓存，调用方需确保 `Next` 或错误路径配置正确。
