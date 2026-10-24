# ADR-0016: 幂等中间件保留语义、命名归位

## 背景

ADR-0013 决定"按 Fiber 官方幂等中间件的文件与类型结构重建" `internal/middleware/global/idempotency`。重建完成后留下了两处副作用：

- **三层入口**：`middleware.Middleware.Idempotency` → `global.Middleware.Idempotency` → `idempotency.New`。其中路由级入口 `middleware.Middleware.Idempotency` 全仓库零调用（幂等只在 `engine.Use` 全局挂载），却导出了 3 个类型别名与 2 个转发函数。
- **框架指涉残留**：9 处注释以 Fiber 为参照描述行为。项目已无 Fiber 依赖，这些措辞会让后续阅读者误判当前技术栈。

## 决策

1. **删除死入口**：移除 `internal/middleware/idempotency.go`（类型别名与转发函数）。
2. **单一入口**：幂等中间件只由 `global.NewIdempotency(store, opts...)` 构造，且只在 `engine.Use` 全局挂载。
3. **按能力描述注释**：把"与 Fiber 同构 / Fiber MemoryLock 行为一致 / Fiber 使用 msgp"等表述改为对行为的直接描述。
4. **语义完整保留**：ADR-0013 的键缺省透传、无锁读缓存、按键加锁、锁内二次读、回放状态码/响应头/响应体、失败不缓存、UUID 长度校验、作用域摘要等条款继续有效。

## 后果

- 正向：一个能力只保留一个门；幂等行为的修改点唯一；读代码不再需要知道 Fiber 曾存在。
- 负向：`middleware.IdempotencyConfig` / `IdempotencyStorage` / `IdempotencyLocker` 三个导出别名被移除，任何外部引用需改用 `idempotency.Config` / `Storage` / `Locker`。
- 约束：ADR-0013 的语义条款仍然有效；本条仅收窄其"按 Fiber 文件结构重建"的表述范围——结构可以对齐业界参考实现，命名与注释必须描述本项目自身。
