# 0006. Gin 架构深化与原生管道接缝整合

## 上下文与决策

为提升代码库的模块深度（Depth）、局部性（Locality）与杠杆率（Leverage），解决既有架构中 Handler 重复编排错误渲染、手动实例化 JSON 解码器以及硬编码服务生命周期的摩擦点，决定对 Gin Web 引擎管道进行以下 4 项深化改造：

1. **集中式错误处理管道 (Centralized Error Pipeline)**：
   建立 `middleware.ErrorHandler` 作为统一错误处理接缝。所有 Feature Handler 与中间件在遇到故障时统一调用 `_ = c.Error(err); return`，由后置错误中间件集中解包 `samber/oops` 业务码、数据库约束、多语言 i18n 翻译及校验错误字典，并协同 `AccessLog` 输出完整的结构化日志。
2. **原生 JSON 序列化标准对齐 (Native JSON Alignment)**：
   保持 Gin 原生默认的 `encoding/json` 标准库实现，杜绝引入第三方 `unsafe` 运行时 hack 依赖（如 `goccy/go-json`）。在典型 RESTful API 中，请求瓶颈绝大多数集中于数据库 I/O 与网络往返，标准库在现代 Go 版本下具备极高稳定性、完全零适配胶水代码，并享有 Go 演进带来的持续内建优化。
3. **统一请求绑定与参数校验接缝 (Unified Binding & Validation Seam)**：
   废弃浅层 `validator.Binder` 包装结构体，将业务规则（`usernameslug`、`e164`、`notreserved`）直接注册至 Gin 全局 `binding.Validator`。各 Handler 统一调用 Gin 原生 `c.ShouldBindJSON(&dto)`。
4. **强类型服务配置与生命周期接缝深化 (Server Lifecycle Depth)**：
   将分散硬编码的 HTTP 读取、写入、空闲与停机超时预算抽象并沉淀至 `internal/config/config.go`，由 `internal/bootstrap/server.go` 集中消费，对外仅暴露单一强内聚的 `Application.Serve(ctx)` 阻塞式服务接口。
