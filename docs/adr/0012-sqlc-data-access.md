# ADR-0012: 采用 sqlc 作为唯一数据访问层

## 背景

数据访问此前由 Bun ORM 承担：实体标签、通用 Active Record 泛型引擎、动态列拼装与生命周期钩子共同构成了第二套「隐式 SQL」。查询无法静态审计，字段变更靠编译器与运行时双重漂移，且与 `schemas/` 声明式 DDL 形成两套事实源。

## 决策

1. **SQL 单一事实源**：所有业务读写收敛到 `internal/database/queries/*.sql`，由 sqlc 1.31.1 基于 `internal/database/schemas/` 离线生成 `internal/database/sqlc`（`pgx/v5`）。生成物提交仓库，`just sqlc-diff` 纳入 `just check` 守门。
2. **生成类型即实体**：`sqlc.User` / `sqlc.Account` 直接进入 Service 与 DTO 投影，不建立 Repository 包装层，不维护 ORM 实体；跨切片复用下沉 `internal/shared/`。
3. **事务由 Context 承载查询集**：`database.DB.RunInTx` 用 `pgx.BeginFunc` 开事务并把 `*sqlc.Queries` 注入 Context；嵌套调用复用最外层事务。`AfterCommit` 登记不可回滚的副作用（如删除被替换的物理文件），回滚时自动丢弃。
4. **写路径一次往返**：局部更新合并为单条 `UPDATE ... RETURNING *`；设置走 `COALESCE`，显式清空走 `clear_*` 布尔标记；删除/软删除用 `:execrows`，零行即 NotFound。
5. **读路径无 N+1**：列表分页用 `count(*) OVER ()` 单查询取总数；关联数据按 ID 集合批量查询；流式导出与搜索索引用物理分块，禁止逐行回查。
6. **类型 override 固定契约**：`timestamptz` → `time.Time`（可空为 `*time.Time`），`jsonb` → `map[string]any`，`uuid` → `string`，`"user".role/status` → `internal/shared/enums`。
7. **认证平台与存储解耦**：`authentication.Guard[T any]` 通过 `func(T) Identity` 投影主体 ID、角色与自定义声明，数据库行类型无需实现任何平台方法。

## 后果

- 正向：SQL 可静态审计、生成物与 DDL 强一致、无隐式查询与逐字段写放大；`go.mod` 彻底移除 Bun 及其方言依赖。
- 负向：复杂动态排序无法任意拼接，只能在 SQL 中枚举白名单排序臂；新增查询需先改 `.sql` 再生成，多一步 `just sqlc`。
- 约束：软删除没有隐式作用域，所有读路径必须显式 `deleted_at IS NULL`；生成文件禁止手改。
