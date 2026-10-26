# 查询约定

1. `database/schemas/` 是 DDL 唯一事实源（版本化产物见 `database/migrations/`），`database/queries/` 是 SQL 唯一落点。
2. 生成物 `internal/database/sqlc/` 由 `just sqlc` 产生，禁止手改；`just sqlc-diff` 在 `just check` 中守门。
3. 读取软删除表必须显式 `deleted_at IS NULL`，删除一律写 `deleted_at = now()` 并用 `:execrows` 暴露影响行数。
4. 可选过滤统一 `sqlc.narg('x')::text`；排序走 `CASE WHEN @sort_key = '...'` 固定臂，禁止拼接 SQL。
5. 局部更新收敛为单条语句：设置用 `COALESCE`，显式清空用 `clear_*` 布尔标记，`RETURNING *` 直接回传整行。
6. 列表分页用 `count(*) OVER ()` 单查询取总数；大批量写入用 `unnest` 单语句完成。
