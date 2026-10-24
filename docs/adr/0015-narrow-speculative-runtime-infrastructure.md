# ADR-0015: 收窄投机性运行时基础设施（Temporal 与清理任务）

## 背景

仓库里存在两套没有任何真实调用方的"能力"：

- Temporal 子系统（`platform/temporal`、`config/temporal.go`、`console/temporal.go`）只服务于 `features/system/workflow.go` 一个工作流，而该工作流在源码注释中自称"示例性"，与 `queue` 的 `system:cleanup_temp_files` 任务职责重复。没有任何代码启动这个工作流：唯一入口是先手工运行 `temporal:work` worker，再手工触发 workflow。
- `system:cleanup_temp_files` 由 `bootstrap/schedules.go` 每日 03:00 调度，但 `Handle` 只输出日志，不执行任何清理。真正的清理实现位于上述示例工作流中。

Excel 管线改为纯流式写出后不再产生 `.cache/excel` 临时文件，清理任务因此失去唯一的生产者；删除示例工作流后，Temporal worker 也不再有任何可注册的 workflow 或 activity。

## 决策

1. **移除 Temporal 子系统**：删除 `internal/platform/temporal`、`internal/config/temporal.go`、`internal/console/temporal.go`、`features/system/workflow.go`、`/readyz` 的 temporal 探针、`TEMPORAL_*` 环境变量、`just work-temporal` 与 `go.temporal.io` 依赖。
2. **移除业务级死任务**：删除 `features/system/job.go`、`bootstrap/schedules.go` 及对应测试；`queue:work` 不再打开存储磁盘，也不再注册系统任务。
3. **保留队列调度框架**：`platform/queue` 的 `Schedule` / `Scheduler` 是有测试覆盖的通用平台能力，本次只清空业务条目，不拆除框架。

## 后果

- 正向：二进制不再链接 Temporal SDK；启动路径少一个可选子系统；不再存在"每日定时删除目录"的空实现——它一旦被补全就会是一个危险的隐式删除动作。
- 负向：需要真正的分布式持久化工作流时，必须重新引入依赖并新写 ADR；`queue:work` 的 scheduler runner 当前没有条目，`--without-scheduler` 在行为上暂无差异。
- 约束：`/readyz` 不再返回 `temporal` 检查项；部署清单与 `.env` 需同步移除 `TEMPORAL_*`；`system:cleanup_temp_files` 任务名不再可用，历史队列中的残留任务会被 worker 视为未知任务。
