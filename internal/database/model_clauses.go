package database

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/uptrace/bun"
)

// ensureBaseColumns 保证在追加计算列（如 WithCount、WithAggregate、WithExists）且此前未曾显式调用 Select(...) 时，
// 自动保留主表的所有字段（?TableAlias.*），避免 Bun ORM 因覆盖默认列导致模型实体字段被清空。
func (q *Query[T]) ensureBaseColumns() *Query[T] {
	if !q.hasExplicitSelect {
		q.sel = q.sel.ColumnExpr("?TableAlias.*")
		q.hasExplicitSelect = true
	}
	return q
}

// Select 指定查询投影列（例如 "id", "email"），重置默认的模型投影列。
func (q *Query[T]) Select(columns ...string) *Query[T] {
	if len(columns) > 0 {
		q.sel = q.sel.Column(columns...)
		q.hasExplicitSelect = true
	}
	return q
}

// SelectExpr 增加计算列或表达式投影（例如 COUNT(*) AS total 或 COALESCE(title, "") AS title）。
func (q *Query[T]) SelectExpr(expr string, args ...any) *Query[T] {
	q.sel = q.sel.ColumnExpr(expr, args...)
	return q
}

// SelectRaw 增加原始 SELECT 计算列投影（SelectExpr 的流式别名，对标 Laravel selectRaw）。
func (q *Query[T]) SelectRaw(expr string, args ...any) *Query[T] {
	return q.SelectExpr(expr, args...)
}

// SelectSub 将子查询作为计算列投影（对标 Laravel selectSub）。
// 例如: SelectSub(subQuery, "latest_order_date")
func (q *Query[T]) SelectSub(subquery *bun.SelectQuery, alias string) *Query[T] {
	if subquery != nil && alias != "" {
		q.sel = q.sel.ColumnExpr("(?) AS ?", subquery, bun.Ident(alias))
	}
	return q
}

// Distinct 增加 DISTINCT 关键字，消除重复结果。
func (q *Query[T]) Distinct() *Query[T] {
	q.sel = q.sel.Distinct()
	return q
}

// DistinctOn 增加 PostgreSQL 原生 DISTINCT ON (...) 约束。
func (q *Query[T]) DistinctOn(expr string, args ...any) *Query[T] {
	q.sel = q.sel.DistinctOn(expr, args...)
	return q
}

// GroupBy 增加 GROUP BY 聚合分组字段。
func (q *Query[T]) GroupBy(columns ...string) *Query[T] {
	if len(columns) > 0 {
		q.sel = q.sel.Group(columns...)
	}
	return q
}

// GroupByExpr 增加原始表达式作为 GROUP BY 分组依据（例如 DATE_TRUNC("month", created_at)）。
func (q *Query[T]) GroupByExpr(expr string, args ...any) *Query[T] {
	if expr != "" {
		q.sel = q.sel.GroupExpr(expr, args...)
	}
	return q
}

// Having 增加 HAVING 分组过滤条件。
func (q *Query[T]) Having(query string, args ...any) *Query[T] {
	q.sel = q.sel.Having(query, args...)
	return q
}

// HavingOr 增加 OR 连接的 HAVING 分组过滤条件。
func (q *Query[T]) HavingOr(query string, args ...any) *Query[T] {
	q.sel = q.sel.Having("OR "+query, args...)
	return q
}

// HavingBetween 增加 HAVING 分组范围过滤（HAVING column BETWEEN min AND max）。
func (q *Query[T]) HavingBetween(column string, minVal, maxVal any) *Query[T] {
	q.sel = q.sel.Having("? BETWEEN ? AND ?", bun.Ident(column), minVal, maxVal)
	return q
}

// HavingRaw 增加原始 HAVING 条件表达式。
func (q *Query[T]) HavingRaw(expr string, args ...any) *Query[T] {
	q.sel = q.sel.Having(expr, args...)
	return q
}

// Where 增加 WHERE 条件。
func (q *Query[T]) Where(query string, args ...any) *Query[T] {
	q.sel = q.sel.Where(query, args...)
	return q
}

// WhereOr 增加 OR WHERE 条件。
func (q *Query[T]) WhereOr(query string, args ...any) *Query[T] {
	q.sel = q.sel.WhereOr(query, args...)
	return q
}

// WhereIf 当 condition 为 true 时才追加 WHERE 条件。
func (q *Query[T]) WhereIf(condition bool, query string, args ...any) *Query[T] {
	if condition {
		return q.Where(query, args...)
	}
	return q
}

func (q *Query[T]) withSubSelect(sub *bun.SelectQuery) *Query[T] {
	return &Query[T]{
		db:                q.db,
		baseDB:            q.baseDB,
		sel:               sub,
		withTrashed:       q.withTrashed,
		onlyTrashed:       q.onlyTrashed,
		chunkSize:         q.chunkSize,
		hasExplicitSelect: q.hasExplicitSelect,
	}
}

func (q *Query[T]) whereGroup(sep string, fn func(*Query[T]) *Query[T]) *Query[T] {
	if fn == nil {
		return q
	}
	q.sel = q.sel.WhereGroup(sep, func(sq *bun.SelectQuery) *bun.SelectQuery {
		subQ := fn(q.withSubSelect(sq))
		return subQ.sel
	})
	return q
}

// WhereGroup 构造带括号的 WHERE 逻辑分组，例如 WHERE (a = 1 AND b = 2)。
func (q *Query[T]) WhereGroup(fn func(*Query[T]) *Query[T]) *Query[T] {
	return q.whereGroup(" AND ", fn)
}

// WhereOrGroup 构造带括号的 OR WHERE 逻辑分组，例如 WHERE ... OR (c = 3 AND d = 4)。
func (q *Query[T]) WhereOrGroup(fn func(*Query[T]) *Query[T]) *Query[T] {
	return q.whereGroup(" OR ", fn)
}

var allowedClauseOperators = map[string]string{
	"=":              "=",
	">":              ">",
	">=":             ">=",
	"<":              "<",
	"<=":             "<=",
	"!=":             "!=",
	"<>":             "<>",
	"LIKE":           "LIKE",
	"ILIKE":          "ILIKE",
	"NOT LIKE":       "NOT LIKE",
	"NOT ILIKE":      "NOT ILIKE",
	"SIMILAR TO":     "SIMILAR TO",
	"NOT SIMILAR TO": "NOT SIMILAR TO",
	"IS":             "IS",
	"IS NOT":         "IS NOT",
	"IN":             "IN",
	"NOT IN":         "NOT IN",
}

// sanitizeClauseOperator 过滤并规范化通用 SQL 比较操作符，杜绝非法注入。
func sanitizeClauseOperator(op string) string {
	clean := strings.ToUpper(strings.Join(strings.Fields(op), " "))
	if valid, ok := allowedClauseOperators[clean]; ok {
		return valid
	}
	return "="
}

// WhereAny 对多个字段应用相同的比较逻辑，满足任一字段即匹配（对标 Laravel 13 whereAny）。
// 例如: WhereAny([]string{"name", "email"}, "ILIKE", "%keyword%")
func (q *Query[T]) WhereAny(columns []string, operator string, arg any) *Query[T] {
	if len(columns) == 0 {
		return q
	}
	op := sanitizeClauseOperator(operator)
	return q.WhereGroup(func(sub *Query[T]) *Query[T] {
		for i, col := range columns {
			cond := fmt.Sprintf("%s %s ?", sanitizeIdent(col), op)
			if i == 0 {
				sub = sub.Where(cond, arg)
			} else {
				sub = sub.WhereOr(cond, arg)
			}
		}
		return sub
	})
}

// WhereAll 对多个字段应用相同的比较逻辑，必须同时满足所有字段才匹配（对标 Laravel 13 whereAll）。
func (q *Query[T]) WhereAll(columns []string, operator string, arg any) *Query[T] {
	if len(columns) == 0 {
		return q
	}
	op := sanitizeClauseOperator(operator)
	return q.WhereGroup(func(sub *Query[T]) *Query[T] {
		for _, col := range columns {
			sub = sub.Where(fmt.Sprintf("%s %s ?", sanitizeIdent(col), op), arg)
		}
		return sub
	})
}

// WhereDate 针对日期部分进行比较（忽略时间），对标 Laravel whereDate。
func (q *Query[T]) WhereDate(column string, date any) *Query[T] {
	return q.Where("DATE("+sanitizeIdent(column)+") = ?", date)
}

// WhereYear 针对年份部分进行比较，对标 Laravel whereYear。
func (q *Query[T]) WhereYear(column string, year int) *Query[T] {
	return q.Where("EXTRACT(YEAR FROM "+sanitizeIdent(column)+") = ?", year)
}

// WhereMonth 针对月份部分进行比较 (1-12)，对标 Laravel whereMonth。
func (q *Query[T]) WhereMonth(column string, month int) *Query[T] {
	return q.Where("EXTRACT(MONTH FROM "+sanitizeIdent(column)+") = ?", month)
}

// WhereDay 针对月份中的天数进行比较 (1-31)，对标 Laravel whereDay。
func (q *Query[T]) WhereDay(column string, day int) *Query[T] {
	return q.Where("EXTRACT(DAY FROM "+sanitizeIdent(column)+") = ?", day)
}

// WhereJSONContains 检查 JSONB 列是否包含指定 JSON 结构（Postgres column @> ?::jsonb）。
func (q *Query[T]) WhereJSONContains(column string, val any) *Query[T] {
	return q.Where(sanitizeIdent(column)+" @> ?::jsonb", val)
}

// WhereJSONLength 检查 JSONB 数组字段的元素数量，对标 Laravel whereJsonLength。
func (q *Query[T]) WhereJSONLength(column string, operator string, length int) *Query[T] {
	op := sanitizeClauseOperator(operator)
	return q.Where("jsonb_array_length("+sanitizeIdent(column)+") "+op+" ?", length)
}

// When 根据 condition 条件链式执行对应的分支函数（对标 Laravel when）。
func (q *Query[T]) When(condition bool, applyFn func(*Query[T]) *Query[T], fallbackFn ...func(*Query[T]) *Query[T]) *Query[T] {
	if condition && applyFn != nil {
		return applyFn(q)
	}
	if !condition && len(fallbackFn) > 0 && fallbackFn[0] != nil {
		return fallbackFn[0](q)
	}
	return q
}

// Unless 当 condition 为 false 时链式执行对应分支（对标 Laravel unless，即 When(!condition)）。
func (q *Query[T]) Unless(condition bool, applyFn func(*Query[T]) *Query[T], fallbackFn ...func(*Query[T]) *Query[T]) *Query[T] {
	return q.When(!condition, applyFn, fallbackFn...)
}

// Tap 在流式链式调用中无缝透传并观察当前查询器（如记录调试日志），不打断流式调用链。
func (q *Query[T]) Tap(fn func(*Query[T])) *Query[T] {
	if fn != nil {
		fn(q)
	}
	return q
}

// WhereIn 封装 IN 过滤；若 slice 为空切片则注入 1 = 0 永假防线，保证空条件安全返回空结果。
func (q *Query[T]) WhereIn(column string, slice any) *Query[T] {
	if isSliceEmpty(slice) {
		return q.Where("1 = 0")
	}
	return q.Where(column+" IN (?)", bun.List(slice))
}

// WhereNotIn 封装 NOT IN 过滤；若 slice 为空切片则保持查询不变（不增加过滤）。
func (q *Query[T]) WhereNotIn(column string, slice any) *Query[T] {
	if isSliceEmpty(slice) {
		return q
	}
	return q.Where(column+" NOT IN (?)", bun.List(slice))
}

// WhereNull 封装 IS NULL 过滤。
func (q *Query[T]) WhereNull(column string) *Query[T] {
	return q.Where(column + " IS NULL")
}

// WhereNotNull 封装 IS NOT NULL 过滤。
func (q *Query[T]) WhereNotNull(column string) *Query[T] {
	return q.Where(column + " IS NOT NULL")
}

// WhereBetween 增加数值或时间范围 BETWEEN 过滤（包含边界值）。
func (q *Query[T]) WhereBetween(column string, minVal, maxVal any) *Query[T] {
	return q.Where(column+" BETWEEN ? AND ?", minVal, maxVal)
}

// WhereNotBetween 增加数值或时间范围 NOT BETWEEN 过滤。
func (q *Query[T]) WhereNotBetween(column string, minVal, maxVal any) *Query[T] {
	return q.Where(column+" NOT BETWEEN ? AND ?", minVal, maxVal)
}

// WhereColumn 增加双列值比较过滤（如比较 updated_at > created_at），自动转义列名抵御 SQL 注入。
func (q *Query[T]) WhereColumn(firstColumn, operator, secondColumn string) *Query[T] {
	op := sanitizeClauseOperator(operator)
	return q.Where("? "+op+" ?", bun.Ident(firstColumn), bun.Ident(secondColumn))
}

// WhereILike 增加不区分大小写的模糊匹配过滤（PostgreSQL 原生 ILIKE）。
func (q *Query[T]) WhereILike(column, pattern string) *Query[T] {
	return q.Where(column+" ILIKE ?", pattern)
}

// WhereNot 增加取反过滤条件（即 NOT (...)）。
func (q *Query[T]) WhereNot(query string, args ...any) *Query[T] {
	return q.Where("NOT ("+query+")", args...)
}

// WhereExists 追加 EXISTS (?) 子查询过滤。
func (q *Query[T]) WhereExists(subquery *bun.SelectQuery) *Query[T] {
	if subquery != nil {
		q.sel = q.sel.Where("EXISTS (?)", subquery)
	}
	return q
}

// OrWhereExists 增加 OR 连接的 EXISTS (?) 子查询过滤。
func (q *Query[T]) OrWhereExists(subquery *bun.SelectQuery) *Query[T] {
	if subquery != nil {
		q.sel = q.sel.WhereOr("EXISTS (?)", subquery)
	}
	return q
}

// WhereNotExists 追加 NOT EXISTS (?) 子查询过滤。
func (q *Query[T]) WhereNotExists(subquery *bun.SelectQuery) *Query[T] {
	if subquery != nil {
		q.sel = q.sel.Where("NOT EXISTS (?)", subquery)
	}
	return q
}

// OrWhereNotExists 增加 OR 连接的 NOT EXISTS (?) 子查询过滤。
func (q *Query[T]) OrWhereNotExists(subquery *bun.SelectQuery) *Query[T] {
	if subquery != nil {
		q.sel = q.sel.WhereOr("NOT EXISTS (?)", subquery)
	}
	return q
}

// WhereSub 增加列与子查询的标量比较过滤（例如 WHERE amount > (SELECT AVG(amount) FROM ...)）。
func (q *Query[T]) WhereSub(column string, operator string, subquery *bun.SelectQuery) *Query[T] {
	if subquery != nil {
		op := sanitizeClauseOperator(operator)
		q.sel = q.sel.Where("? "+op+" (?)", bun.Ident(column), subquery)
	}
	return q
}

// OrWhereSub 增加 OR 连接的列与子查询标量比较过滤。
func (q *Query[T]) OrWhereSub(column string, operator string, subquery *bun.SelectQuery) *Query[T] {
	if subquery != nil {
		op := sanitizeClauseOperator(operator)
		q.sel = q.sel.WhereOr("? "+op+" (?)", bun.Ident(column), subquery)
	}
	return q
}

// WhereInSub 增加列在子查询结果集中的 IN 过滤（例如 WHERE id IN (SELECT user_id FROM ...)）。
func (q *Query[T]) WhereInSub(column string, subquery *bun.SelectQuery) *Query[T] {
	return q.WhereSub(column, "IN", subquery)
}

// OrWhereInSub 增加 OR 连接的 IN 子查询过滤。
func (q *Query[T]) OrWhereInSub(column string, subquery *bun.SelectQuery) *Query[T] {
	return q.OrWhereSub(column, "IN", subquery)
}

// WhereNotInSub 增加列不在子查询结果集中的 NOT IN 过滤。
func (q *Query[T]) WhereNotInSub(column string, subquery *bun.SelectQuery) *Query[T] {
	return q.WhereSub(column, "NOT IN", subquery)
}

// OrWhereNotInSub 增加 OR 连接的 NOT IN 子查询过滤。
func (q *Query[T]) OrWhereNotInSub(column string, subquery *bun.SelectQuery) *Query[T] {
	return q.OrWhereSub(column, "NOT IN", subquery)
}

// FromSub 将子查询作为派生表进行查询（对标 Laravel fromSub）。
// 例如: FromSub(subQuery, "active_users") 生成 FROM (SELECT ...) AS "active_users"
func (q *Query[T]) FromSub(subquery *bun.SelectQuery, alias string) *Query[T] {
	if subquery != nil && alias != "" {
		q.sel = q.sel.TableExpr("(?) AS ?", subquery, bun.Ident(alias))
	}
	return q
}

// WithCTE 增加通用表表达式（Common Table Expression, WITH name AS (...)），单次 SQL 往返完成复杂流水线。
func (q *Query[T]) WithCTE(name string, cteQuery bun.Query) *Query[T] {
	if name != "" && cteQuery != nil {
		q.sel = q.sel.With(name, cteQuery)
	}
	return q
}

// Union 增加 UNION 联合查询，合并不同查询结果集。
func (q *Query[T]) Union(subquery *bun.SelectQuery) *Query[T] {
	if subquery != nil {
		q.sel = q.sel.Union(subquery)
	}
	return q
}

// UnionAll 增加 UNION ALL 联合查询，保留重复项高性能合并结果集。
func (q *Query[T]) UnionAll(subquery *bun.SelectQuery) *Query[T] {
	if subquery != nil {
		q.sel = q.sel.UnionAll(subquery)
	}
	return q
}

// WhereRow 增加多列元组值比较（Tuple Comparison），常用于多字段游标分页与复合键过滤。
// 例如: WhereRow([]string{"created_at", "id"}, "<", lastCreatedAt, lastID) 生成 ("created_at", "id") < (?, ?)
func (q *Query[T]) WhereRow(columns []string, operator string, values ...any) *Query[T] {
	if len(columns) == 0 || len(values) == 0 {
		return q
	}
	op := sanitizeClauseOperator(operator)
	q.sel = q.sel.Where("(?) "+op+" (?)", bun.List(toIdents(columns)), bun.List(values))
	return q
}

// LockForUpdate 声明悲观排他锁 (FOR UPDATE)。
func (q *Query[T]) LockForUpdate() *Query[T] {
	q.sel = q.sel.For("UPDATE")
	return q
}

// SharedLock 声明共享锁 (FOR SHARE)。
func (q *Query[T]) SharedLock() *Query[T] {
	q.sel = q.sel.For("SHARE")
	return q
}

// isSliceEmpty 检查给定值是否为切片或数组且长度为 0。
func isSliceEmpty(slice any) bool {
	if slice == nil {
		return false
	}
	val := reflect.ValueOf(slice)
	switch val.Kind() {
	case reflect.Slice, reflect.Array:
		return val.Len() == 0
	default:
		return false
	}
}

// Order 增加排序字段。
func (q *Query[T]) Order(orders ...string) *Query[T] {
	if len(orders) > 0 {
		q.sel = q.sel.Order(orders...)
	}
	return q
}

// OrderExpr 使用原生 SQL 表达式排序（支持 ?TableAlias 与参数占位符）。
func (q *Query[T]) OrderExpr(query string, args ...any) *Query[T] {
	if query != "" {
		q.sel = q.sel.OrderExpr(query, args...)
	}
	return q
}

// Latest 声明按时间倒序排列（缺省排序列为 created_at）。
func (q *Query[T]) Latest(column ...string) *Query[T] {
	q.sel = q.sel.OrderExpr("?TableAlias.? DESC", bun.Ident(defaultString("created_at", column)))
	return q
}

// Oldest 声明按时间正序排列（缺省排序列为 created_at）。
func (q *Query[T]) Oldest(column ...string) *Query[T] {
	q.sel = q.sel.OrderExpr("?TableAlias.? ASC", bun.Ident(defaultString("created_at", column)))
	return q
}

// InRandomOrder 声明按随机顺序检索（PostgreSQL RANDOM()），对标 Laravel inRandomOrder。
func (q *Query[T]) InRandomOrder() *Query[T] {
	q.sel = q.sel.OrderExpr("RANDOM()")
	return q
}

// Offset 设置偏移量。
func (q *Query[T]) Offset(offset int) *Query[T] {
	if offset > 0 {
		q.sel = q.sel.Offset(int64(offset))
	}
	return q
}

// Limit 设置每页数量。
func (q *Query[T]) Limit(limit int) *Query[T] {
	if limit > 0 {
		q.sel = q.sel.Limit(int64(limit))
	}
	return q
}

// Scope 应用一个或多个查询作用域。
func (q *Query[T]) Scope(scopes ...ScopeFunc[T]) *Query[T] {
	for _, scope := range scopes {
		if scope != nil {
			q = scope(q)
		}
	}
	return q
}

// WithTrashed 声明包含被软删除的数据。
func (q *Query[T]) WithTrashed() *Query[T] {
	q.withTrashed = true
	q.onlyTrashed = false
	return q
}

// OnlyTrashed 声明仅查询被软删除的数据。
func (q *Query[T]) OnlyTrashed() *Query[T] {
	q.onlyTrashed = true
	q.withTrashed = false
	return q
}

// WithoutTrashed 显式恢复默认排除软删除状态。
func (q *Query[T]) WithoutTrashed() *Query[T] {
	q.withTrashed = false
	q.onlyTrashed = false
	return q
}

// BuildSelect 返回应用了软删除规则后的底层 *bun.SelectQuery，便于单元测试或高级扩展。
func (q *Query[T]) BuildSelect() *bun.SelectQuery {
	return q.applySoftDelete()
}

// applySoftDelete 检查当前模型是否支持软删除，若未声明 WithTrashed 则在克隆的底层查询上追加软删除过滤。
func (q *Query[T]) applySoftDelete() *bun.SelectQuery {
	if q.sel == nil {
		return nil
	}
	sel := q.sel.Clone()
	if !isSoftDeletable[T]() {
		return sel
	}
	if q.onlyTrashed {
		return sel.Where("?TableAlias.deleted_at IS NOT NULL")
	}
	if q.withTrashed {
		return sel
	}
	return sel.Where("?TableAlias.deleted_at IS NULL")
}
