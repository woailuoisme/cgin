# cgin - AI Coding Agent 研发与协作总纲 (AGENTS.md)

本文档是针对接入本仓库的所有 AI Coding Agent（如 Claude Code, Antigravity, Cursor, Windsurf 等）的核心认知与工程操作总纲。Agent 在本仓库执行任何探索、设计、编码、重构、审查或排错任务时，**必须严格遵守本规范及所索引的专责规则**。

---

## 1. 项目概览与技术拓扑 (Project Overview & Tech Stack)

`cgin` 是一个基于现代化 Go 语言规范构建的企业级高并发 RESTful Web API 骨架。

- **运行时与语言规范**：[Go 1.27+](file:///Users/seaside/Projects/backend/go/gin-sqlc/go.mod) 现代标准，开启严格并发安全与类型安全。
- **Web 引擎**：[Gin](file:///Users/seaside/Projects/backend/go/gin-sqlc/internal/bootstrap/engine.go) (`github.com/gin-gonic/gin`)，统一错误恢复、链路追踪、CORS 与限流中间件。
- **中间件生态**：传输层全面采用 [gin-contrib](https://github.com/gin-contrib) 官方中间件 —— `cors` / `authz` / `cache` / `requestid` / `timeout` / `logger` / `i18n` / `graceful`；官方未覆盖的能力（错误信封、幂等、限流、请求体配额、探针降噪、资源级鉴权）由 `internal/middleware` 自建，归属见 §3.1 对照表。
- **数据访问**：[sqlc 1.31.1](file:///Users/seaside/Projects/backend/go/gin-sqlc/sqlc.yaml) 离线生成类型安全查询集（`pgx/v5`，输出 `internal/database/sqlc`）+ [pgxpool 原生连接池](file:///Users/seaside/Projects/backend/go/gin-sqlc/internal/database/client.go)，底层运行 PostgreSQL 18。
- **数据库迁移 (Migrations)**：[Atlas CLI](file:///Users/seaside/Projects/backend/go/gin-sqlc/atlas.hcl)，采用声明式 Schema 驱动的版本化 SQL 迁移。
- **缓存体系**：[Valkey (Redis 兼容)](file:///Users/seaside/Projects/backend/go/gin-sqlc/pkg/cache/cache.go) + 高性能驱动 (`redis/rueidis`)。
- **鉴权与授权**：无状态 JWT 令牌 (`golang-jwt/jwt/v5`) + 基于 [Casbin v3](file:///Users/seaside/Projects/backend/go/gin-sqlc/pkg/authorization/authz.go) 的 RBAC 静态权限矩阵。
- **领域事件与异步任务**：[Watermill](file:///Users/seaside/Projects/backend/go/gin-sqlc/pkg/eventbus/manager.go) (`ThreeDotsLabs/watermill`) 事件总线与 CQRS 路由 + [Asynq](file:///Users/seaside/Projects/backend/go/gin-sqlc/pkg/queue/client.go) (`hibiken/asynq`) 周期任务与重试队列。
- **实时通信**：[Centrifugo](file:///Users/seaside/Projects/backend/go/gin-sqlc/pkg/realtime/manager.go) 连接凭据签发与订阅鉴权代理，频道命名与分发规则收敛在 service 层。
- **CLI 命令行体系**：基于 [`spf13/cobra`](file:///Users/seaside/Projects/backend/go/gin-sqlc/internal/console/root.go) 的现代工业级分层命令行系统，分组管理子命令并提供隐式 Artisan 风格冒号别名兼容。
- **对象存储与邮件**：S3/MinIO (`minio/minio-go/v7`) 与本地存储抽象；Mailpit 邮件测试系统 (`wneessen/go-mail`)。
- **结构化日志与错误处理**：[Zerolog](https://github.com/rs/zerolog) (`github.com/rs/zerolog`) 零分配链式结构化上下文日志（统一以 `*zerolog.Logger` 注入，`logger.Nop()` 兜底）；`samber/oops` 业务错误堆栈与语义化错误码。
- **时间推导与数据格式化**：[Carbon v2](https://github.com/golang-module/carbon) (`golang-module/carbon/v2`) 链式时间计算与多语言相对时间 + [go-humanize](https://github.com/dustin/go-humanize) (`dustin/go-humanize`) 字节容量与大数字千分位格式化。
- **高精度金融与货币体系**：基于 [`shopspring/decimal`](https://github.com/shopspring/decimal) 与 [pkg/money](file:///Users/seaside/Projects/backend/go/gin-sqlc/pkg/money/money.go) 的不可变 Money 值对象体系，原生映射 Postgres `NUMERIC`，提供 ISO-4217 元数据、金融舍入、Fowler 无损均摊及收单网关适配。
- **接口契约与文档**：基于 [api.md](file:///Users/seaside/Projects/backend/go/gin-sqlc/api.md) 契约，使用 `swag` 自动化生成 OpenAPI 规范并嵌入二进制。

---

## 2. 核心工作流与 Just 任务契约 (Core Workflows & Just Tasks)

本仓库所有常规开发、检查、迁移与测试操作**强制统一通过根目录 [justfile](file:///Users/seaside/Projects/backend/go/gin-sqlc/justfile) 执行**。严禁 Agent 私自拼接长命令，避免漏传环境变量或构建标签。

> **终端执行红线**：所有命令在终端执行时必须前缀 `rtk`（如 `rtk just check`），且**绝对严禁执行 `cd` 改变工作目录**。

| 命令分类 | 执行指令 | 功能说明与关键约束 |
| :--- | :--- | :--- |
| **开发与运行** | `just dev` | 多进程并发热重载（基于 `concurrently` 同时启动 API 与 Worker 双 Air 实例）。 |
| | `just dev-api` | 仅启动 API 服务的 Air 热重载（自动释放端口占用）。 |
| | `just dev-worker` | 仅启动 Worker 服务的 Air 热重载（基于 `.air.worker.toml`）。 |
| | `just run [ARGS]` | 直接运行 API 服务（如 `just run --port 3400`）。 |
| | `just work [ARGS]` | 直接运行 Worker 服务（如 `just work -q high:3,default:1`）。 |
| | `just cli [ARGS]` | 运行 CLI 控制台命令（如 `just cli route:list` 或 `just cli make:crud blog`，支持别名 `just artisan`）。 |
| | `just init [ARGS]` | 一键全流程环境初始化（.env、双密钥、Postgres 数据库创建、Atlas 迁移、初始播种与 S3 桶检测）。 |
| | `just build` | 编译可执行文件至 `bin/cgin`，自动注入 version/commit/date 元数据。 |
| **静态检查与格式化** | `just fmt` | 代码自动格式化并就地改写文件（基于 `golangci-lint fmt`）。 |
| | `just fmt-check` | 仅检查格式是否规范，不改写文件。 |
| | `just lint` | 静态检查（严格保持 0 警告 0 报错）。 |
| **测试套件** | `just test` | 执行全量单元测试（内存级快速执行）。 |
| | `just test-int` | 执行集成测试（需本地真实 Postgres/Valkey/Mailpit，打标签 `-tags=integration`）。 |
| | `just test-all` | 同时执行单元测试与集成测试。 |
| | `just coverage` | 统计覆盖率（必须携带 `-coverpkg=./internal/...` 解决外部测试包插桩问题）。 |
| **三级门禁** | `just check` | **交付前强制执行**：`fmt-check` + `lint` + `sqlc-diff` + `test`。 |
| | `just ci` | 完整 CI 门禁：`fmt-check` + `lint` + `sqlc-diff` + `test-all`。 |
| **查询生成** | `just sqlc` | 由 `database/schemas` + `database/queries` 离线生成 `internal/database/sqlc`。 |
| | `just sqlc-diff` | 校验生成物与 SQL 事实源一致，禁止手改生成文件。 |
| **数据库与迁移** | `just migrate` | 应用未执行的 Atlas 版本迁移（依赖本地 Postgres 5432）。 |
| | `just migrate-diff <name>` | 对比 `database/schemas` 的声明式 DDL 生成新的版本迁移文件。 |
| | `just migrate-hash` | 重算 `database/migrations` 的哈希完整性校验链。 |
| | `just seed` | 幂等注入首个管理员数据。 |
| | `just fresh` | 破坏性重建数据库并重新播种（需确认；自动化脚本可用 `just fresh-force`）。 |
| **API 文档生成** | `just docs` | 使用 `swag` 解析生成客户端规范（`shared/apidocs/swagger.json`）与管理端规范（`shared/apidocs/admin_swagger.json`）。 |

---

## 3. 分层优先架构与依赖红线 (Layer-First Architecture & Boundaries)

项目采用**分层优先（Layer-First）**设计，依赖方向固定为 `route → handler → service → dto`，边界严格：

```
cmd/api/main.go                 # 应用程序唯一入口，控制权移交 internal/console
internal/                       # 私有实现层（分层优先：route → handler → service → dto 单向依赖）
├── bootstrap/                  # 依赖组装（组合根）、引擎初始化与服务生命周期管理
├── config/                     # 基于 caarlos0/env 的强类型环境配置加载
├── console/                    # Artisan 风格 CLI 命令（serve, db:seed, db:fresh）
├── database/                   # 连接池、事务工作单元（RunInTx）与生成查询集
│   ├── sqlc/                   # sqlc 生成物（禁止手改）
│   └── seeders/                # 幂等数据播种器
├── dto/                        # 传输契约层（package dto）：一个模块一个文件
│   ├── admin/                  # 管理端契约（package admindto）
│   └── auth.go · profile.go · payment.go · realtime.go · system.go · user.go
├── enum/                       # 核心领域枚举契约（Role / UserStatus / OTPType / ProviderID）
├── event/                      # 领域事件模型：跨切片事实载荷（auth.go · payment.go）
├── export/                     # 领域导出用例（XLSX 组装，基于 pkg/excel）
├── handler/                    # 传输适配层（package handler）
│   ├── admin/                  # package adminhandler
│   └── auth.go · profile.go · payment.go · realtime.go · system.go
├── import/                     # 领域导入用例（预留空层：待 pkg/excel 补齐读取能力）
├── index/                      # 全文检索索引器定义与集中装配（基于 pkg/search）
├── job/                        # 异步任务签名与处理器（Asynq Job，基于 pkg/queue）
├── listener/                   # 领域事件监听器注册（向 pkg/eventbus 挂载订阅）
├── media/                      # 媒体处理内核：上传验证、命名、路径推导、管道与多态附件（基于 pkg/storage）
├── middleware/                 # 全局与路由级 HTTP 中间件（认证、限流、CORS、监控）
├── notification/               # 跨切片通知类型与数据载荷（otp.go · security_alert.go）
│   └── store/                  # 通知消息的关系型持久化渠道（package store）
├── realtime/                   # 实时频道命名、订阅鉴权与分发规则（基于 pkg/realtime）
├── route/                      # 路由注册层（package route）
│   ├── admin/                  # package adminroute
│   └── auth.go · profile.go · payment.go · realtime.go · system.go
├── schedule/                   # 周期任务编排（预留空层：待 worker 接线方向确定）
├── service/                    # 用例层（package service）
│   ├── admin/                  # package adminservice
│   └── auth*.go · profile.go · payment.go · realtime*.go · system.go · verification.go
└── user/                       # 用户内核：User View、字段约束、角色策略、注册与基础查询契约
database/                       # SQL 事实源与版本化迁移资产（Atlas / sqlc 共用根）
├── schemas/                    # 声明式目标 DDL（唯一事实源）
├── queries/                    # SQL 唯一落点（sqlc 输入，业务读写全部集中于此）
└── migrations/                 # Atlas 版本化迁移与 atlas.sum 完整性校验链
pkg/                            # 可复用的横切基础设施与跨切片基座（对外公开的原子组件）
├── authentication/             # 统一认证守卫 (Guard)、双令牌生命周期与用户认证门禁
├── authorization/              # Casbin RBAC 静态策略与权限校验（策略矩阵见本目录下 rbac/policy.csv）
├── cache/                      # Valkey 客户端、分级缓存与分布式锁
├── errs/                       # Oops 错误模型、全局错误码与多语言错误渲染
├── eventbus/                   # Watermill 事件总线、CQRS 路由与日志适配
├── excel/                      # 流式 XLSX 导出
├── geoip/                      # ip2region 离线归属地检索与热重载
├── hasher/                     # Argon2id / Bcrypt 密码哈希
├── i18n/                       # 语言装载与文案渲染门面（基于 go-i18n，底层同 gin-contrib/i18n）
├── logger/                     # Zerolog 结构化日志构建、脱敏与请求作用域透传
├── mailer/                     # SMTP 邮件发送抽象（Mailpit / Resend / Log）
├── money/                      # 不可变高精度货币值对象 (Money)、ISO-4217 元数据、金融舍入与无损分摊
├── notification/               # 通知渠道抽象、异步工作池与多通道驱动
├── payment/                    # 微信/支付宝收单网关与 Fake 渠道
├── pdf/                        # maroto 报表渲染与字体注册
├── qrcode/                     # 二维码生成与渲染
├── queue/                      # Asynq 队列客户端、调度与中间件管线
├── ready/                      # 就绪探针契约与组件状态机
├── realtime/                   # Centrifugo 凭据签发与订阅代理
├── resilience/                 # 重试、退避与熔断
├── response/                   # HTTP 成功响应封装（扁平对象与分页信封）
├── search/                     # 全文检索客户端与索引器装配
├── storage/                    # 本地与 S3/MinIO 对象存储适配
├── telemetry/                  # OpenTelemetry 初始化与 zerolog Hook 桥接
├── validator/                  # Go-Playground 校验器与国际化翻译集成
└── wechat/                     # 微信公众号/小程序接口适配
shared/                         # 纯静态资产层（仅嵌入式 OpenAPI 规范；业务内核全部沉入 internal）
└── apidocs/                    # 嵌入式 OpenAPI 规范与文档契约
test/                           # 外部测试包（测试代码与业务逻辑彻底解耦，镜像同层目录）
├── architecture/               # 分层依赖红线守卫（layering_test.go）
├── integration/                # 真实外部依赖的集成测试（声明 //go:build integration）
├── internal/                   # 镜像 internal/ 各层用例（bootstrap · config · console · database · dto · enum · export · index · job · listener · media · middleware · notification · realtime · route · service · user）
├── pkg/                        # 镜像 pkg/ 各原子基础设施组件的单元测试
├── shared/                     # 镜像 shared/ 静态资产的单元测试（apidocs）
└── testutil/                   # 公共测试夹具与辅助函数
```

### 3.1 中间件 ↔ gin-contrib 对照表 (Middleware Provenance)

传输层能力优先薄封装 gin-contrib 官方中间件；只有官方生态确实没有对应实现时，才在 `internal/middleware` 自建，并在注释中标注归属。**清理"重复造轮子"时，下表「自建」一栏不得删除。**

| 能力 | 实现归属 | 代码位置 |
| :--- | :--- | :--- |
| 跨域预检 | 官方 `gin-contrib/cors` | `internal/middleware/global/cors.go` |
| 路径级 Casbin 鉴权 | 官方 `gin-contrib/authz` | `internal/middleware/auth.go` `Authz()` |
| 文档响应缓存 | 官方 `gin-contrib/cache` | `internal/bootstrap/docs.go` |
| 请求标识 | 官方 `gin-contrib/requestid` | `internal/middleware/global/observability.go` `RequestID()` |
| 请求超时 | 官方 `gin-contrib/timeout` | `internal/middleware/global/timeout.go` |
| 访问日志 | 官方 `gin-contrib/logger` | `internal/middleware/global/observability.go` `AccessLog()` |
| 多语言协商 | 官方 `gin-contrib/i18n` | `internal/middleware/global/i18n.go` |
| 优雅停机 | 官方 `gin-contrib/graceful` | `internal/bootstrap/server.go` |
| **统一错误信封 + 校验文案翻译** | 自建（官方无对应物） | `internal/middleware/global/error.go` |
| **panic → 统一 500 信封** | 自建（官方 Recovery 绕过错误码契约） | `internal/middleware/global/observability.go` `Recovery()` |
| **请求体配额 / JSON 类型强校验** | 自建（官方无对应物） | `internal/middleware/global/security.go` |
| **Idempotency-Key 幂等重放** | 自建（官方无对应物） | `internal/middleware/global/idempotency.go` |
| **分级滑动窗口限流** | 自建（官方无对应物） | `internal/middleware/throttle.go` |
| **探针路由降噪** | 自建（官方 skipper 无该语义） | `internal/middleware/global/observability.go` |
| **资源+动作级鉴权** | 自建（官方 authz 只支持 URL+Method） | `internal/middleware/auth.go` `Can()` |

### 🚨 架构分层绝对红线

1. **分层单向依赖（Layer-First 红线）**：依赖方向固定为 `route → handler → service → dto`（`dto` 仅被读取，不反向依赖任何层）。
   - **绝对禁止** `service` 反向 import `handler` 或 `route`，也禁止同层内不同模块包相互 import（如 `adminhandler` 与 `handler`）；
   - **路由注册必须收敛在 `internal/route/` 单包**：`handler` 包内严禁再出现路由注册代码；
   - `dto` 层是全系统唯一允许被 `handler` 与 `service` 共同引用的传输契约层，**严禁**在 `dto` 中 import `handler`、`service` 或 `route`；
   - 每层目录**不建模块文件夹**：一个模块一个文件直接落在层根（`internal/<layer>/<module>.go`）；管理端专属内容统一收纳进各层的 `admin/` 子目录（`internal/<layer>/admin/<module>.go`），并共用 `admindto` / `adminhandler` / `adminservice` / `adminroute` 包名。
2. **跨 Feature 数据共享统一沉淀至 `internal/` 内核**：跨切片复用的用户视图、字段约束、角色策略与基础 Repository 已收敛在 [internal/user/](file:///Users/seaside/Projects/backend/go/gin-sqlc/internal/user/)，媒体处理内核已收敛在 [internal/media/](file:///Users/seaside/Projects/backend/go/gin-sqlc/internal/media/)。`shared/` 仅保留嵌入式 OpenAPI 静态资产（`apidocs/`），**严禁承接任何业务代码**。新增跨包复用的数据投影必须先入内核再被引用，严禁在下游各自复制。
3. **分层职责绝对隔离**：
   - **Handler**：只负责 HTTP 参数解析绑定、入参校验调度、调用 Service、组装 `response.OK`。**严禁在 Handler 中编写核心业务逻辑或直接操作 ORM/SQL**。
   - **Service**：承载纯业务规则与事务协调，输入输出均为强类型 DTO/Model。通过返回 `samber/oops` 结构化错误传递失败原因。
   - **DTO**：只承载契约结构、校验标签、投影函数与查询归一化，**严禁出现 I/O、事务或任何业务副作用**。
   - **Platform (`pkg/`)**：基础设施层必须保持纯粹独立，**严禁 Platform 反向依赖任何 `internal/` 业务包或 `shared/` 业务包**。
4. **Middleware 传输层协议隔离与就地内聚**：
   - `internal/middleware/` 是专属于 HTTP Delivery/Transport 传输层的通用横切逻辑（认证、鉴权、限流、CORS、监控、全局错误转换），与 Web 协议强绑定。**严禁将 `middleware` 迁入协议无关的 `shared/`**，避免污染纯净的领域共享内核。
   - 通用传输层中间件集中于 `internal/middleware/`；若未来存在仅限单一领域使用的特定业务拦截器，**直接就地内聚于对应的 `internal/handler/<module>.go` 内部**。
5. **统一错误与契约协议基座定位 (`pkg/errs`)**：
   - `pkg/errs` 是全系统严格对齐 [api.md](file:///Users/seaside/Projects/backend/go/gin-sqlc/api.md) 契约的**统一错误与传输协议基座**（定义机器可读错误码 `Code`、封装 `samber/oops` 堆栈追踪、PostgreSQL 驱动约束翻译及 `Render` HTTP 最终写回）。
   - **绝对严禁将 `pkg/errs` 迁入 `shared/`**：防止 `render.go` 将 `gin.Context` 与 Web 传输协议污染进纯领域共享内核，同时杜绝 `storage`、`mailer`、`queue` 等底层技术原子反向依赖业务共享包。
   - 业务错误码（如 `AUTH_*`、`PAYMENT_*`）作为系统对外契约常量统一集中于 `pkg/errs/codes.go`，由状态码映射字典 `statuses` 集中内聚管理，避免分散配置导致的契约漂移。
6. **常量与枚举作用域规约与就近内聚原则 (`internal/enum`)**：
   - **核心领域枚举集中于 `internal/enum`**：仅当枚举属于全系统跨垂直切片共享的**核心业务领域实体概念**（如 `Role`、`UserStatus`、`OTPType`、`ProviderID`）时，方可在 `internal/enum` 中定义，且必须使用强类型 `type X string` 并提供 `IsValid()` / `String()` 等纯粹契约。该包是不导入任何 `cgin/` 内部包的零依赖叶子包，`internal/` 各层统一引用这一唯一来源；`pkg/` 与 `shared/` 严禁依赖它。
   - **严禁「集中式常量垃圾桶」反模式 (No God Enum / No Centralized Technical Constants)**：严禁将任何特定技术实现常量、中间件键名、基础设施细节直接堆入 `internal/enum`：
     - **Gin Context Key**：必须就近定义在对应传输层中间件或认证包内部，推荐使用非导出类型（如 `type authCtxKey struct{}`）或私有常量，通过强类型 Helper 函数（如 `auth.FromContext(c)`）对外暴露存取，严禁全局暴露原生 Context 字符串键。
     - **Cache Key / Redis 前缀**：必须就近封装在具体的 Cache Repository 或 Platform Cache 内部，外部调用方通过高阶仓储方法存取，严禁将原始键名前缀向外部全局扩散。
     - **异步任务与队列名 (Queue / Job Tasks)**：必须就近内聚在调度该任务的 Feature 切片或 `pkg/queue` 内部，各业务独立维护 Job 签名。
     - **严禁投机性死代码与 UI 徽标元数据 (YAGNI)**：严禁预先编写全系统 0 调用的投机性常量文件，严禁在后端枚举中硬编码前端展示类元数据（如 CSS 徽标颜色、选项字典映射）。
7. **HTTP DTO 单一文件与四大后缀强类型绑定规约 (`dto.go`)**：
   - **单一 `dto.go` 文件集中内聚**：每个领域契约文件（`internal/dto/<module>.go`）内的 HTTP 请求与响应传输对象集中内聚，彻底废除 `request.go` 与 `response.go` 物理双文件，将该领域的全部传输契约与模型投影函数（`To<Entity>Resource`）单点内聚。
   - **投影函数导出约定**：被 `service` 或 `handler` 跨包调用的投影函数与请求体派生方法必须导出（`ToUserResource`、`ProjectUser`、`FieldSet`、`AdminFieldSet`），禁止跨包引用未导出符号。
   - **四大后缀与 Gin 绑定映射红线**：
     1. **`*Body`**：对应请求体载荷（JSON / Form），Handler 统一使用 `c.ShouldBindJSON(&body)` 绑定；
     2. **`*PathParams`**：对应路由 URI 路径参数（必须附带 `uri:"..."` 标签与校验规则），Handler 统一使用 `c.ShouldBindUri(&pathParams)` 强类型绑定与校验。**绝对严禁在 Handler 中使用裸调 `c.Param("id")` 提取路径参数**；
     3. **`*QueryParams`**：对应 URL Query 查询参数（必须附带 `form:"..."` 与 `json:"..."` 标签），Handler 统一使用 `c.ShouldBindQuery(&queryParams)` 绑定；
     4. **`*Resource`**：对应响应数据单体视图（如 `UserResource`）；分页列表必须显式声明 `*PageResource` 结构体（内含 `items []<Entity>Resource` 与 `meta response.Meta`），确保 Swagger/OpenAPI 无歧义精准解析。
   - **全系统 `camelCase` 契约硬性红线**：所有 DTO 的 `json:"..."`、`form:"..."` 与 `uri:"..."` 字段标签统一使用小驼峰（`camelCase`），严格与 `api.md` RESTful 契约保持一致。
8. **数据校验分层与数据库查重/存在性校验纪律 (Validation & DB Verification Discipline)**：
   - **严禁在 Transport/Validator 层挂载数据库 I/O 规则（绝对红线）**：严禁在 `pkg/validator` 中实现或注册类似 Laravel 的 `unique` / `exists` 数据库标签规则：
     - **杜绝 TOCTOU 竞态漏洞**：校验期的 `SELECT COUNT(*)` 无法保证并发安全，两并发请求校验通过后仍会触发数据库冲突；
     - **架构分层绝对纯粹**：保持 Validator 为无状态纯内存操作，严禁反向依赖数据库连接池或 ORM 实例；
     - **避免连接池枯竭与无谓 I/O**：防止恶意无效请求在反序列化阶段即耗尽 DB 连接池。
   - **唯一性与外键存在性标准实现范式**：
     1. **唯一性冲突 (Unique)**：由 Service 层业务查重判断 + **PostgreSQL 原生唯一索引（`UNIQUE INDEX`）作为终极防线**。底层 `23505 (pgErrUniqueViolation)` 由 `pkg/errs/render.go` 集中自动翻译为 409 Conflict 或特定业务错误码，业务层无需手动处理底层 SQL 异常。
     2. **外键与关联存在性 (Exists)**：遵循「查完即用 (Fetch-Once)」原则，在 Service 层使用 sqlc 生成查询直接获取实体；若实体不存在立即返回 `errs.NotFound`，彻底杜绝在 Validator 查一次存在性、在 Service 又查一次实体的低效双重查询。
     3. **复杂跨字段复合校验**：统一通过让 DTO 实现 `Validatable` 接口（`Validate() error`），由 Gin 校验器自动触发，保持声明式 Tag 验证基础格式与命令式 Hook 验证业务联动职责清晰。

---

## 4. Go 1.27+ 现代语言规范与 Samber 工具集 (Go Standards & Samber)

在开发、重构或审查本仓库代码时，必须严格遵守并自动应用来自 `samber/cc-skills-golang` 的核心标准与 Go 1.27+ 现代语言规范：

### 4.1 代码风格与惯用模式
- **规范遵循**：严格遵循 Effective Go 规范，函数短小精炼、控制流扁平、卫语句（Guard Clauses）优先。
- **命名规范**：遵循 Go 惯用 CamelCase 驼峰命名，接收者命名简短且语义明确（如 `s *Service`），避免无意义的 `this`/`self`。
- **现代特性**：充分运用现代 Go 特性（`for range` 整数/函数迭代、`slices`/`maps`/`cmp` 标准库包、测试中的 `t.Context()` 等）。

### 4.2 结构化错误处理与函数式转换
- **结构化错误**：全面使用 `samber/oops` 进行业务错误包装，显式附带业务错误码（如 `oops.Code(errs.CodeUserNotFound).Wrap(err)`），方便最外层错误中间件统一拦截与状态码映射。
- **切片与 Map 转换**：优先使用 `samber/lo` 提供的函数式工具（`lo.Map`, `lo.Filter`, `lo.KeyBy`, `lo.Ternary`, `lo.Uniq` 等），严禁书写冗长的命令式 `for` 循环遍历拼装代码。
- **容量预分配**：所有切片转换与 Map 构建必须预分配空间（如 `make([]T, 0, len(source))`），优化热路径内存分配。

### 4.3 并发安全与 Context 传递
- **严格 Context 贯穿**：所有涉 I/O 操作（数据库查询、缓存存取、网络调用、存储读写）的方法签名**第一个参数必须显式传递 `ctx context.Context`**。
- **生命周期受控**：禁止在请求链路中裸起无法追踪生命周期的 Goroutine；如需后台处理，必须使用带有超时和取消机制的独立派生 Context 或安全 Worker 池，且必须具备 Panic 恢复守卫。

### 4.4 结构化日志纪律与 fmt 输出禁令 (Structured Logging & No fmt.Print)
- **绝对严禁 `fmt.Print*` 输出**：核心运行时代码与业务逻辑层（`internal/` 目录除 `internal/console/` 外）**严禁使用 `fmt.Print`、`fmt.Println`、`fmt.Printf` 或传统标准库 `log` 输出运行日志**。
- **全链路结构化日志**：所有服务运行事件、告警、安全审计与排错记录必须统一通过 `cgin/pkg/logger` 输出，优先使用 `logger.FromContext(ctx)` 确保请求级 `request_id` 全链路串联。
- **合法例外场景**：
  - 允许使用 `fmt.Sprintf`、`fmt.Errorf` 等纯内存字符串格式化与错误包装函数。
  - 命令行 CLI 交互层（`cmd/api` 与 `internal/console/`）允许使用 `fmt` 或终端美化库向用户控制台输出帮助文本与退出错误。
- **静态门禁拦截**：`.golangci.yml` 启用 `forbidigo` 规则，对业务代码中的 `fmt.Print*` 实行自动化拦截阻断。

### 4.5 注释精简中文与代码字符串全英文约束 (Chinese Comments & English Code Literals)
- **注释保留精简中文**：所有包注释、结构体/类型文档注释、函数/方法文档注释、行内逻辑说明**统一强制保留精炼、地道的简体中文**，严禁书写英文注释，严禁中英文机械混杂。
- **代码中所有字符串字面量、操作名、结构化日志与底层错误统一为标准英文（绝对红线）**：
  - 代码中的所有字符串字面量、底层驱动与库操作名（如 `"write local object"`、`"open upload file"`、`"read s3 object"` 等）、Go 哨兵错误以及 `errors.New` / `fmt.Errorf` 错误信息**必须全部统一使用标准英文**。**绝对严禁在代码中出现中文字符串字面量、中文操作名或中文内部错误**。
  - **结构化运行日志全英文**：所有结构化运行日志文本统一强制使用标准英文表达（如 `log.Warn().Err(err).Msg("failed to write cache")`），以契合现代化 APM 检索分析与业界标准，彻底杜绝中文日志说明。
  - 面向终端用户的多语言展示文案与字段校验提示，**绝对严禁直接硬编码在业务代码中**，必须统一由 `message/` 目录下的 i18n 资源文件（TOML）承载。
- **Swagger 接口文档全中文**：所有 API 接口文档注解（`@Summary`、`@Description`、`@Tags`、字段说明）必须为纯中文表达，面向国内调用与研发协同保持地道语义。
- **语法级标识符保持英文**：Go 语言语法标识符（包名、结构体名、字段名、函数/方法名、接口名）以及协议级标签（`json:"..."`、`validate:"..."`、HTTP 头部字段）严格遵循标准英文命名与 CamelCase 驼峰规范。

| 代码要素分类 | 语言硬性约束 | 规范示范 | 违规反例（严禁出现） |
| :--- | :--- | :--- | :--- |
| **代码注释 (`// ...`)** | 强制精简中文 | `// ensureDir 递归创建目标文件的父目录。` | `// ensureDir recursively creates...` |
| **代码内字符串与操作名** | **强制标准英文** | `opFailed("write local object", path, err)` | `opFailed("写入本地对象", path, err)` |
| **底层与内部错误信息** | **强制标准英文** | `errors.New("storage: local root path cannot be empty")` | `errors.New("本地存储根目录不能为空")` |
| **结构化运行日志** | **强制标准英文** | `logger.Warn("failed to write cache")` | `logger.Warn("回源数据写入缓存失败")` |
| **API 接口文档注解** | 强制地道中文 | `// @Summary 邮箱密码登录` | `// @Summary Email password login` |
| **终端用户错误提示** | 统一 i18n 资源 | `message/zh-CN/*.toml`, `message/en/*.toml` | 在业务代码中直接硬编码中文提示 |

### 4.6 时间、容量与数据格式化红线规范 (Time, Capacity & Formatting Discipline)
- **数据库生成类型与 DTO 原生时间红线（绝对红线）**：sqlc 生成结构体与对外传输对象 DTO（`dto.go`）**必须严格使用 Go 原生 `time.Time`**（由 `sqlc.yaml` 的 `timestamptz` override 统一保证）。严禁在数据库映射与对外契约中引入 `carbon.Carbon` / `carbon.DateTime` 等第三方包装类型，彻底确保 pgx/PostgreSQL `timestamptz` 原生驱动与 OpenAPI RFC 3339 序列化的极致性能、类型安全与契约稳定。
- **业务时间推导统一 Carbon**：在业务逻辑层（Service）、计划任务（Worker）、CLI 命令与报表导出（Excel）中，凡涉及复杂时间计算（如有效期加减、封禁时间推导、自然日跨度）、相对时间展示（`DiffForHumans`）以及非标日期字符串解析时，**统一使用 `github.com/golang-module/carbon/v2`**（全局时区与 Locale 自动与 `.env` 中 `APP_TIMEZONE` 和 `APP_LOCALE` 同步）。
- **容量与数字人性化统一 Humanize**：在 CLI 报表输出、运维日志、存储元数据与监控统计中，文件/对象字节容量展示（`humanize.Bytes` / `humanize.IBytes`）、大数字千分位分割（`humanize.Comma`）**统一使用 `github.com/dustin/go-humanize`**，严禁手动编写冗余的除法换算或格式化逻辑。

### 4.7 Uber Go 工业级编码规范约束 (Uber Go Style Guide Enforcement)

全项目代码编写、重构与 Review **强制遵循 [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md) 工业级规范**，重点固化以下刚性工程纪律：

1. **接收者类型严格一致性 (Consistent Receiver Type)**：
   - 同一个类型的所有方法，其接收者要么统一使用指针接收者 `(t *Type)`，要么统一使用值接收者 `(t Type)`，**绝对严禁混用**。
   - 凡结构体包含可变状态、互斥锁、内部 Map/Slice、复杂生命周期或大对象，所有方法必须一律使用**指针接收者**。
   - 唯有无状态的微型不可变值对象（如纯坐标、简单度量值）且无修改方法时，方可统一采用值接收者。
2. **单一错误处理原则 (Handle Errors Once / Log OR Return, Never Both)**：
   - 对任何发生的错误，**要么在当前层记录日志并降级处理，要么封装上下文向调用方抛出，绝对严禁既记日志又返回错误**。
   - 杜绝底层与外层多重重复打标造成的日志雪崩与排障噪音。所有底层系统错误统一通过 `samber/oops` 向上冒泡，交由传输层统一拦截渲染与结构化审计。
3. **互斥锁与并发模型纪律 (Zero-value Mutexes & No Value Copy)**：
   - `sync.Mutex` 和 `sync.RWMutex` 必须作为非导出私有字段（如 `mu sync.RWMutex`），**严禁直接导出公开**。
   - 互斥锁零值即完全可用，严禁对其执行多余的指针堆分配或非必要封装。
   - **严禁拷贝包含 Mutex 的结构体**：凡内嵌或拥有互斥锁的结构体，在跨函数、方法或通道传递时**必须严格使用指针传参**。
4. **Channel 尺寸约束与最小化原则 (Channel Buffering: 0 or 1)**：
   - Channel 默认必须为无缓冲通道（Unbuffered / 容量为 0），用于确定性的同步握手协作。
   - 仅在明确用于防阻塞单飞信号（如 HTTP 停机等待、并发结果首个捕获、系统退出通道）时，显式指定缓冲容量为 1（`make(chan error, 1)`）。
   - **绝对严禁随意编写无确切背压依据的魔数大缓冲通道**（如 `make(chan T, 100)`），避免内存无序积压与隐藏的死锁风险。
5. **边界切片与 Map 防御性拷贝 (Copy Slices and Maps at Boundaries)**：
   - 结构体接收外部切片/Map 作为构造入参，或对外公开返回内部内部切片/Map 时，**必须使用浅拷贝或深拷贝进行防御隔离**（如 `slices.Clone` 或预分配拷贝），严禁直接将内部切片底层指针暴露，杜绝外部并发读写引发的数据篡改与 Data Race。
6. **平坦控制流与卫语句优先 (Reduce Nesting & Guard Clauses)**：
   - 严格避免深层 `if-else` 分支嵌套，优先使用错误与边界条件的卫语句提前 `return`，保持主逻辑处于最浅缩进层级。
7. **面向使用者定义最小接口 (Accept Interfaces, Return Structs)**：
   - 服务构造器统一返回具体的结构体指针（如 `*Service`），避免过早抽象导致调用方受制于肥接口；接口由具体的消费方（Caller）在依赖处按需就地定义最小接口契约。

### 4.8 全链路追踪标识与类型前缀 UUIDv7 规范 (Type-Prefixed UUIDv7 Specification)
- **Stripe 风格前缀紧凑 UUIDv7 标准**：全链路请求追踪 ID（`request_id`）统一采用工业界标准的**类型前缀紧凑型 UUIDv7**（形如 `req_<32hex>`，例如 `req_01a0f7382105731198af306440d05dca`）。
- **核心结构优势**：
  1. **实体类型上下文 (`req_`)**：提供即时的业务语义上下文，在跨服务 APM、微服务分布式链路追踪与日志检索中可瞬间识别资源属性，杜绝跨字段误用；
  2. **高精度时序局部性**：基于 RFC 9562 Version 7 规范，高 48 位嵌入毫秒级 Unix 时间戳，天然具备全局单调递增性，彻底消除经典 UUIDv4 在数据库索引与日志检索中的 B-Tree 随机页分裂（Page Split）；
  3. **紧凑与高效存储**：去除连字符（hyphen）压缩为 32 字符十六进制，方便双击全选与 URL/Header 传递；在需要写入数据库原生 `uuid` 字段时，可直接进行二进制转换，零额外开销。

### 4.9 云原生高可用治理与双探针解耦纪律 (Cloud-Native Probes & Resilience)

系统在面临外部依赖（PostgreSQL 18 与 Valkey/Redis）异常与抖动时，遵循严格的云原生分级容灾与治理规范：

#### 1. 双探针职责彻底解耦（避免惊群重启雪崩）
- **致命反模式阻断**：行业里最致命的反模式是“数据库挂了，让整个服务死掉重启”。在微服务多副本场景下，这会导致集群内几百个 Pod 同时反复重启，造成数据库刚缓过来又被瞬间冲垮（引发灾难性的惊群雪崩）。
- **存活探针 [handler.go#L36](file:///Users/seaside/Projects/backend/go/gin-sqlc/internal/handler/system/handler.go#L36-L39) (`GET /livez`)**：
  - 只要 Go 进程自身没发生死锁或僵死，**永远返回 200**。Kubernetes 确认进程存活，绝不会盲目杀死重启容器。
  - **绝对禁令**：严禁在 `/livez` 中绑定数据库或外部依赖连通性。
- **就绪探针 [service.go#L58-L62](file:///Users/seaside/Projects/backend/go/gin-sqlc/internal/service/system/service.go#L58-L62) (`GET /readyz`)**：
  - **数据库 (`ComponentDatabase`, Critical = true)**：一旦 Postgres 断开，`/readyz` 立即返回 `503 Service Unavailable`。Kubernetes/负载均衡器收到 503 后，会瞬间将该实例从服务节点剔除，切断新流量接入，保护终端用户免受报错影响；一旦数据库恢复，探针重新变绿（200），网关瞬间自动将流量重新注入！
  - **Redis 缓存 (`ComponentCache`, Critical = false)**：Redis 属于非致命加速层，如果宕机，`/readyz` 返回 `200 degraded`，系统识别为降级状态，依旧允许接流。

#### 2. 全生命周期闭环保障
- **启动期**：配置错、DB 连不上 $\to$ 快速失败退出（Fail-Fast），绝不带病上线。在 [internal/bootstrap/wire.go](file:///Users/seaside/Projects/backend/go/gin-sqlc/internal/bootstrap/wire.go#L62-L66) 执行 `sqlDB.PingContext(ctx)` 失败即通过 `teardown` 逆序释放资源并以非零退出码（`os.Exit(1)`）退出。
- **运行期网络抖动**：长连接池自适应重试重连，业务请求转为 500 结构化错误，进程长青。底层 `pgx/v5` 与 `rueidis` 连接池内置保活与自动重连，网络恢复后无需人工干预即可自动恢复流量处理。
- **运行期外部服务宕机**：`/readyz` 秒级将流量自动排空摘除，杜绝雪崩扩散；恢复后秒级自动回切。

#### 3. 高频探针日志健康静默纪律 (Probe Health Silence)
- 为避免本地开发与生产容器探针高频轮询（每 2~3 秒一次）导致控制台与集中式日志系统被正常心跳日志刷屏，中间件 [internal/middleware/global/observability.go](file:///Users/seaside/Projects/backend/go/gin-sqlc/internal/middleware/global/observability.go#L64-L67) **在 `status < 400` 时对 `/livez` 与 `/readyz` 实行完全静默，不记录访问日志**。
- 一旦探针出现任何异常（`status >= 400` 或 `>= 500`），强制保留 `Warn` 或 `Error` 级别结构化日志与堆栈，确保 APM 告警与应急排障无死角。

---

## 5. API 契约、错误模型与 Swagger 踩坑规避 (API Contract & Swagger)

所有对外暴露的 REST 接口必须严格遵循根目录 [api.md](file:///Users/seaside/Projects/backend/go/gin-sqlc/api.md) 契约：

### 5.1 路径与响应规范
- **路径语义**：遵循 Better-Auth 端点命名风格（如 `POST /v1/auth/sign-in/email`, `GET /v1/users/me`），资源路径使用小写复数名词。
- **成功响应**：
  - 单一资源/操作响应：直接返回该业务对象本身，不额外包裹无意义的 `data` 信封（由 `response.OK(c, resource)` 处理）。
  - 分页列表响应：统一包含 `items` 数组与 `meta` 分页元数据对象（由 `response.Paginated(c, items, meta)` 处理）。
- **统一错误响应模型**：非 2xx 响应一律输出统一 JSON 结构 `{ "code": "STRING_CODE", "message": "Localized message" }`。
  - 参数校验失败返回 `422 Unprocessable Entity`，并在响应中附加 `errors: { "fieldName": ["localized error message"] }` 错误归集字典。

### 5.2 前后台双文档体系架构 (Dual-Doc System Architecture)

系统采用前后台严格隔离的双 OpenAPI / Scalar 文档架构：
1. **客户端文档 (`/docs`)**：
   - 宿主页挂载在 `/docs`，规范接口为 `/docs/openapi.json`（对应静态文件 `shared/apidocs/swagger.json`）。
   - 根信息在 `cmd/api/main.go` 中声明（Title 为 `cgin API`）。
   - **严格收录范围**：仅包含客户端自助与认证端点（`auth`、`profile`、`realtime`）。分层扁平化后同层包混装两侧端点，收录范围改由 Swagger Tag 白名单收敛（`认证中心,个人资料,实时通信`），并辅以 `--exclude internal/handler/admin,...` 降低解析开销。
2. **管理端文档 (`/docs/admin`)**：
   - 宿主页挂载在 `/docs/admin`，规范接口为 `/docs/admin/openapi.json`（对应静态文件 `shared/apidocs/admin_swagger.json`）。
   - 根信息在 `cmd/api/admin_doc.go` 中声明（Title 为 `cgin Admin API`）。
   - **严格收录范围**：仅收录管理端 API（如 `internal/route/admin`、`internal/handler/admin`），收敛手段同为 Tag 白名单（`用户管理,管理员认证`）。新增端点必须写 `@Tags`，否则会同时从两套文档消失——该约束由 `test/shared/apidocs` 的注解覆盖测试守门。
   - **解除旧约束**：取消原 ADR-0005 中“管理端不添加 swag 注解”的旧约束，管理端 Handler 与 DTO 必须全量规范书写 Swag 注解。
3. **环境与访问控制**：
   - 两份文档统一受 `ENABLE_DOCS` 配置项控制，生产环境未开启时均返回 404。
4. **组件定义命名 (Component Definitions)**：
   - 分层扁平化后 DTO 统一收敛到 `dto` / `admindto` 两个包，OpenAPI 组件定义名相应为 `dto.<Struct>` 与 `admindto.<Struct>`（客户端与管理端各自独立）。
   - 按组件名生成客户端 SDK / 类型定义的消费方，在包结构变更后必须重新执行一次生成同步。

### 5.3 Swag / OpenAPI 注解编写约束与规范 (Swag Annotation Standards)

对外暴露的所有 API Handler 与传输对象 DTO 必须严格遵守以下注解规范，保证生成的 OpenAPI/Swagger 文档精确、一致且符合中文本土化标准：

1. **Handler 路由注解约束**：
   - **全中文简要描述**：`@Tags`、`@Summary`、`@Description` 必须统一使用简要地道的中文，严禁使用英文缩写或生硬机翻。
   - **领域标签规范 (`@Tags`)**：统一按业务领域划分简短中文标签（例如 `认证中心`、`个人资料`、`用户管理`、`系统探针`）。
   - **Tag 顶层顺序声明 (`@tag.x-order`)**：所有 Tag 必须在 `cmd/api/main.go` 中统一定义，声明 `@tag.name`、`@tag.description` 并显式标注 `@tag.x-order <序号>`，确保左侧侧边栏 Tag 分组绝对有序。
   - **职责分离**：`@Summary` 在 10~15 字内清晰概括接口动作；`@Description` 简要说明核心业务规则、前置校验条件或防枚举等安全设计。
   - **只声明成功响应，不使用 `@Failure`**：接口文档保持极简聚焦，统一**不声明 `@Failure` 注解**，**只保留 `@Success`**。异常响应已由全局统一错误中间件契约与标准错误模型覆盖，杜绝在每个 Handler 上机械堆叠冗余的 `@Failure` 注解。
   - **显式端点排序 (`@x-order`)**：所有对外接口必须显式声明 `// @x-order <序号>` 明确同 Tag 下的展示顺序（升序排列，如注册为 1、登录为 2、会话为 4 等），配合文档引擎消除默认路径字母序乱序。

2. **DTO 结构体与字段注解约束**：
   - **结构体命名与后缀**：严格遵循四大后缀标准（`*Body`, `*PathParams`, `*QueryParams`, `*Resource`, `*PageResource`）。
   - **精简中文注释**：DTO 中每个对外暴露的字段上方必须附带精简的单行中文业务描述，严禁无注释裸字段。
   - **真实示例标签 (`example:"..."`)**：所有字段必须在 struct tag 中给出 `example:"..."`，示例值必须符合格式校验规则（如合法邮箱、UUID、E.164 手机号、示例密码等）。
   - **字符串枚举必须全量列举 (`enums:"..."`)**：针对所有固定取值范围的字符串枚举字段（如用户角色、账号状态等）：
     - 必须在 struct tag 中显式声明 `enums:"val1,val2,..."` 列出全部候选枚举；
     - 必须在字段的中文注释中明确罗列每个候选枚举的语义（如 `// 用户权限角色 (user: 普通用户, admin: 系统管理员)`）。
   - **强制小驼峰标签 (`camelCase`)**：所有 DTO 的 `json:"..."`、`form:"..."` 与 `uri:"..."` 标签必须为标准小驼峰命名（`camelCase`），严格对齐 `api.md` RESTful 契约。

3. **标准代码模版示范**：
   ```go
   // Handler 注解示范
   // @Summary      邮箱密码登录
   // @Description  邮箱不存在与口令错误返回统一错误码，防止账号枚举；登录成功后颁发双令牌。
   // @Tags         认证中心
   // @Accept       json
   // @Produce      json
   // @Param        payload body SignInEmailBody true "登录凭据"
   // @Success      200 {object} SessionResource "登录成功并颁发会话凭据"
   // @x-order      2
   // @Router       /auth/sign-in/email [post]

   // DTO 字段注解示范 (dto.go)
   type CreateUserBody struct {
       // 用户电子邮箱
       Email string `json:"email" example:"alex@example.com" validate:"required,email,max=254"`
       // 登录口令 (8-128位，须含字母与数字)
       Password string `json:"password" example:"Secret123!" validate:"required,min=8,max=128"`
       // 用户展示名称
       Name string `json:"name" example:"Alex Mercer" validate:"required,min=1,max=120"`
       // 用户权限角色 (user: 普通用户, admin: 系统管理员)
       Role string `json:"role" example:"user" enums:"user,admin" validate:"required,oneof=user admin"`
       // 账号状态 (active: 正常活跃, suspended: 封禁停用)
       Status string `json:"status" example:"active" enums:"active,suspended" validate:"required,oneof=active suspended"`
   }
   ```

### 5.4 Swagger 已知坑点与类型规避 (Known Swagger Pitfalls)
- **注解格式扁平化**：保持扁平化 OpenAPI/Swagger 注解规范，**严禁使用 `oneOf` 冗余包装**。
- ⚠️ **`swag` 跨包类型解析缺陷与别名避坑**：
  - **现象**：`swag` 在解析跨包结构体引用（如跨包直接声明 `{object} user.Profile`）时存在编译器缺陷，极易偶发报 `cannot find type definition` 导致 `just docs` 失败。
  - **强制规则**：涉及跨包引用的结构体，必须在当前 handler 包内定义一层本地同名类型别名（例如在 [internal/dto/profile.go](file:///Users/seaside/Projects/backend/go/gin-sqlc/internal/dto/profile.go) 中显式声明 `type ProfileResource = user.Profile`）。修改或重构相关代码时**严禁误删此类类型别名**！
- **三态字段 Swagger 类型修正**：使用三态值容器 `omitnull.Val[T]` (`aarondl/opt`) 的字段必须显式附加 `swaggertype:"string" extensions:"x-nullable"`（或对应基础标量类型），防止泛型容器被展开为 `{state, val}`。

---

## 6. 数据库与 Atlas 声明式迁移三位一体 (Database & Migrations)

本项目的数据库表结构演进遵循严格的**三位一体强一致性**机制：

```
[database/schemas/] ──(唯一事实源)──> 声明式目标 DDL
             │
             ▼ (通过 `just migrate-diff <name>` 比对生成)
[database/migrations/YYYYMMDD_name.sql] ──(版本化记录)──> 真实执行的增量迁移脚本
             │
             ▼ (同步映射)
[database/queries/*.sql] ──(sqlc generate)──> 类型安全查询集（生成物禁止手改）
```

### 数据库操作硬性准则：
1. **唯一事实源**：[database/schemas/](file:///Users/seaside/Projects/backend/go/gin-sqlc/database/schemas) 是数据库最终预期形态的声明式事实源。新增或修改表结构时，**必须先在此目录中补充完整的 PostgreSQL DDL**。
2. **严禁裸写 DDL**：绝对禁止直接连接数据库手动跑未版本化的 DDL，绝对禁止 Agent 手写虚构的 migration 文件。所有迁移必须运行 `just migrate-diff <feature_name>` 由 Atlas 自动差量比对生成。
3. **哈希完整性**：若发生合并不慎改动了 `database/migrations/` 文件，必须执行 `just migrate-hash` 重建完整性校验链。
4. **SQL 单一事实源**：业务读写只写在 `database/queries/*.sql`；改完必须执行 `just sqlc`，生成物由 `just sqlc-diff` 守门。
5. **事务治理**：`DB.RunInTx` 用 `pgx.BeginFunc` 开事务并把 `*sqlc.Queries` 注入 Context；嵌套调用自动复用最外层事务，`AfterCommit` 承载不可回滚的副作用（如删除旧物理文件）。

---

## 7. 外部黑盒测试架构与三级质量门禁 (Testing & Quality Gates)

### 7.1 测试组织架构
- **外部黑盒包**：所有的测试代码统一存放在根目录 [test/](file:///Users/seaside/Projects/backend/go/gin-sqlc/test/) 下，采用 `package xxx_test` 命名（如 `package auth_test`）。严禁在 `internal/` 目录内散落 `*_test.go` 文件，强制 Agent 从公共调用方视角对 API 进行黑盒校验。
- **构建标签隔离**：
  - 单元测试（Mock / 内存）：无需任何构建标签，执行速度极快。
  - 集成测试（集成真实 Postgres / Valkey / Mailpit）：测试文件首行必须声明 `//go:build integration` 构建标签，仅在运行 `just test-int` 时激活。
- **断言规范**：使用 `github.com/stretchr/testify`，关键前置条件与不可恢复判断使用 `require`（快速失败），普通字段与属性校验使用 `assert`。
- **架构契约测试**：[test/architecture/](file:///Users/seaside/Projects/backend/go/gin-sqlc/test/architecture/) 用 `go/parser` 扫描各层导入，强制 `route → handler → service → dto` 单向依赖与 `pkg/` 纯净性。分层扁平化后同层模块共享一个包，编译器无法拦截反向 import，这条测试就是该红线的执行者——新增跨层调用前先确认它不会变红。

### 7.2 交付前三级质量门禁 (Quality Gates)

Agent 在完成任何功能开发、缺陷修复或结构重构后，**宣布交付前必须依次闭环执行以下质量门禁**：

```mermaid
flowchart LR
    G1["1. 格式化检查<br/>(just fmt-check / fmt)"] --> G2["2. 静态检查<br/>(just lint)"]
    G2 --> G3["3. 单元测试<br/>(just test)"]
    G3 -.->|"涉及持久化/网络流"| G4["4. 真实集成测试<br/>(just test-int)"]
```

1. **格式化与样式**：运行 `just fmt-check`（如有差异执行 `just fmt` 修复）。
2. **静态代码质量**：运行 `just lint`，严格保持 **0 警告、0 报错**。
3. **回归验证**：运行 `just test`，确保单元测试全绿。
4. **集成回归（视场景要求）**：若变更涉及数据库 Model、SQL 查询、缓存或外部服务链路，必须执行 `just test-int` 确保真实集成用例全绿通过。

---

## 8. 规则指针与专责规范协同 (Context Pointers)

Agent 在面对特定专业任务时，必须结合以下位于 [.agents/rules/](file:///Users/seaside/Projects/backend/go/gin-sqlc/.agents/rules/) 下的专责规范执行：

- 🔄 **代码语法探索与跨文件重构** ➔ [ast-grep 规范](file:///Users/seaside/Projects/backend/go/gin-sqlc/.agents/rules/ast-grep.md)
  - *触发条件*：涉及方法签名变更、跨文件重命名、类型定义修改或批量接口迁移。
  - *核心纪律*：**语法级重构唯一工具**；强制执行“定位 ➜ 规则设计 ➜ 沙盒验证 ➜ 批量应用”5 步法；**严禁逐个文件单文件轮询修改，严禁正则批量替换代码**。
- 🛡️ **行为纪律与极简原则** ➔ [coding-discipline 规范](file:///Users/seaside/Projects/backend/go/gin-sqlc/.agents/rules/coding-discipline.md)
  - *触发条件*：编写、修改或重构任何既有业务代码。
  - *核心纪律*：外科手术式最小变更，仅改动与目标直接相关的代码；不猜测需求，遇到分歧主动对齐；严禁单文件迭代循环手工编辑。
- 🛠️ **核心 MCP 工具链协同** ➔ [mcp-tools 规范](file:///Users/seaside/Projects/backend/go/gin-sqlc/.agents/rules/mcp-tools.md)
  - *触发条件*：进行代码拓扑感知、第三方库 API 查询或深度技术调研。
  - *核心选型*：本地调用链探索首选 `codegraph`；第三方依赖权威文档首选 `context7`；前沿深度调研使用 `exa`；时效性 Issue 排查使用 `tavily`。
- 💻 **终端 Shell 与 CLI 命令执行** ➔ [shell 规范](file:///Users/seaside/Projects/backend/go/gin-sqlc/.agents/rules/shell.md)
  - *触发条件*：在终端执行任何构建、测试、检查或文件操作。
  - *核心禁令*：**绝对严禁 `cd` 改变目录**；优先使用 `rg`, `fd` 现代工具；所有命令前缀 `rtk` 降低 Token 消耗。
- 🌐 **跨项目通用基础工程纪律** ➔ [general 规范](file:///Users/seaside/Projects/backend/go/gin-sqlc/.agents/rules/general.md)
  - *核心准则*：默认简体中文交互；所有代码文件与符号引用强制使用可点击 Markdown 链接；提交记录遵循 Conventional Commits 规范。
- 🧭 **Go 技能路由总规则** ➔ [golang-always 规则](file:///Users/seaside/Projects/backend/go/gin-sqlc/.agents/rules/golang-always.mdc)
  - *触发条件*：处理任何 `*.go`、`go.mod` 任务时自动应用，按意图并行加载 Primary 与 Secondary Go skills。
