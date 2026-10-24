# 0010. CLI 命令行体系迁移至 Cobra 工业级最佳实践

## 上下文与决策

此前项目基于 `urfave/cli/v3`（预览版）构建 Artisan 风格命令行系统。随着业务模块扩展（数据库迁移/播种、队列处理/重试、Scout 搜索引擎索引、JWT 密钥生成等），旧实现暴露了层级命令支持薄弱、缺乏原生分组与动态补全、全局状态污染单测、以及直接硬编码输出到 `os.Stdout` 导致测试不可控等缺陷。

依据 Go 工业界最佳实践（Kubernetes、GitHub CLI 标准）及本仓库架构设计原则，全面迁移至 `github.com/spf13/cobra`，确立以下 6 项核心架构契约：

1. **工业级分层拓扑与 Artisan 冒号别名双轨兼容 (Hierarchical Tree with Hidden Colon Aliases)**：
   采用标准分层子命令设计（如 `bgin db migrate`、`bgin queue work`、`bgin scout import`、`bgin jwt secret`）。同时在根命令层注入带 `Hidden: true` 标记的冒号别名（如 `db:migrate`、`queue:work` 等），既保持与现有 `.air.worker.toml`、`justfile` 脚本及 Laravel Artisan 习惯的 100% 向后兼容，又使 `--help` 输出保持整洁聚焦。

2. **零包级全局变量与纯函数工厂构造 (Stateless Factory Pattern with Zero Global State)**：
   拒绝包级全局 `*cobra.Command` 变量与 `init()` 注册。所有命令统一通过工厂函数按需创建（如 `NewRootCommand()`、`newDBCmd()`、`newServeCmd()` 等），Flag 均局部绑定至具体命令实例。多测试用例并发或串行执行时彻底杜绝 Flag 污染与脏状态残留。

3. **I/O 流彻底解耦与纯内存捕获 (I/O Redirection via OutOrStdout & ErrOrStderr)**：
   终端美化输出层（`internal/console/style.go`）与 `tabwriter` 彻底解除对 `os.Stdout`/`os.Stderr` 的硬编码依赖，统一调用 `cmd.OutOrStdout()` 与 `cmd.ErrOrStderr()`。单测中通过 `cmd.SetOut(buf)` 即可 100% 在进程内精确捕获断言终端输出，无需依赖子进程执行或脆弱的文件重定向。

4. **声明式参数校验与错误抑制收敛 (Declarative Args Validation & Error Convergence)**：
   所有子命令全面采用 Cobra 声明式参数校验（如 `cobra.NoArgs`、`cobra.ExactArgs(1)` 等），入参不符合规则时自动阻断。根命令显式配置 `SilenceUsage: true` 与 `SilenceErrors: true`，杜绝运行时业务错误打印冗长帮助文档，并由最外层统一捕获错误退出，消除重复堆栈与杂乱输出。

5. **四大业务分组与动态 Shell 补全增强 (Native Grouping & Dynamic Shell Completions)**：
   引入 4 大原生 `cobra.Group`（`服务与工作进程`、`数据库与迁移`、`搜索与索引`、`系统与安全`），使控制台 `--help` 分组清晰直观。集成原生 `completion` 命令支持 bash/zsh/fish/powershell，并为 `db seed --class` 与 `scout import/flush [model]` 提供精准的动态候选项补全。

6. **彻底清除遗留依赖 (Complete Cleanup of Legacy Dependencies)**：
   从 `go.mod` 与 `go.sum` 中彻底剔除 `github.com/urfave/cli/v3`，全量单元测试与静态门禁（`just check`）保持 0 报警 0 错误。

## As-built 现状（复核记录）

第 1、5 条在落地过程中与 `AGENTS.md` §1 的现行契约合流，实现形态与原文有出入，**以 AGENTS 为准**：

1. **命名与别名**：命令的 `Use` 直接就是 Artisan 冒号名（`db:migrate`、`queue:work`、`make:crud`…），靠 `GroupID` 在 `--help` 里按命名空间分组；未再引入 `bgin db migrate` 层级父命令，也没有 `Hidden: true` 的重复别名。
   冒号名是唯一入口——`justfile` 与 `.air.worker.toml` 都直接调用它，再挂一层父命令只会多一份需要同步维护的命令树。
2. **分组数量与语言**：实际注册 6 个 `cobra.Group`（`db`/`jwt`/`make`/`queue`/`route`/`scout`），而非原文的 4 个中文分组；原文的 4 组已无法覆盖后续新增的 `make`/`route`/`jwt` 命名空间，且分组标题沿用命名空间本身以保证与命令前缀一致。

第 2、3、4、6 条（零包级全局状态与工厂构造、I/O 流解耦、声明式参数校验与错误收敛、剔除 `urfave/cli/v3`）已按原文落地。
