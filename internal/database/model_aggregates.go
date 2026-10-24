package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
)

// ErrMultipleRecordsFound 当查询期望单条记录却检索出多条时抛出（对标 Laravel MultipleRecordsFoundException）。
var ErrMultipleRecordsFound = errors.New("database: multiple records found for sole query")

// First 执行单条记录查询。
func (q *Query[T]) First(ctx context.Context) (*T, error) {
	var item T
	sel := q.BuildSelect()
	if err := sel.Limit(1).Scan(ctx, &item); err != nil {
		return nil, err
	}
	if dt, ok := any(&item).(DirtyTrackable); ok {
		dt.SyncOriginal()
	}
	return &item, nil
}

// Sole 检索恰好一条匹配记录（对标 Laravel sole）。
// 若未找到返回 sql.ErrNoRows；若匹配到 2 条及以上则返回 ErrMultipleRecordsFound。
func (q *Query[T]) Sole(ctx context.Context) (*T, error) {
	items, err := q.Limit(2).Get(ctx)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	if len(items) > 1 {
		return nil, ErrMultipleRecordsFound
	}
	return &items[0], nil
}

// SoleValue 检索恰好一条匹配记录的单列标量值，若未找到返回 sql.ErrNoRows，多条返回 ErrMultipleRecordsFound。
func (q *Query[T]) SoleValue(ctx context.Context, column string) (any, error) {
	item, err := q.Sole(ctx)
	if err != nil {
		return nil, err
	}
	fields, ok := structFields(item)
	if !ok {
		return nil, fmt.Errorf("database: model must be a struct to extract column %q", column)
	}
	if v, ok := fields[column]; ok {
		return v, nil
	}
	for col, v := range fields {
		if strings.EqualFold(col, column) {
			return v, nil
		}
	}
	return nil, fmt.Errorf("database: column %q not found in model", column)
}

// FirstOr 查找满足条件的首条记录；未找到时执行回调函数并返回其结果（对标 Laravel firstOr）。
func (q *Query[T]) FirstOr(ctx context.Context, fallbackFn func() (*T, error)) (*T, error) {
	item, err := q.First(ctx)
	if err == nil {
		return item, nil
	}
	if errors.Is(err, sql.ErrNoRows) && fallbackFn != nil {
		return fallbackFn()
	}
	return nil, err
}

// Find 根据主键 ID 查找单条记录；未找到时返回 sql.ErrNoRows。
func (q *Query[T]) Find(ctx context.Context, id string) (*T, error) {
	return q.Where("?TableAlias.id = ?", id).First(ctx)
}

// FindOr 根据主键查找单条记录；未找到时执行回调函数并返回其结果（对标 Laravel findOr）。
func (q *Query[T]) FindOr(ctx context.Context, id string, fallbackFn func() (*T, error)) (*T, error) {
	return q.Where("?TableAlias.id = ?", id).FirstOr(ctx, fallbackFn)
}

// FindOrFail 根据主键 ID 查找单条记录，若未找到显式返回 sql.ErrNoRows。
func (q *Query[T]) FindOrFail(ctx context.Context, id string) (*T, error) {
	return q.Find(ctx, id)
}

// FindMany 根据一组主键 ID 批量查找记录切片；若传入切片为空则立即短路返回空切片。
func (q *Query[T]) FindMany(ctx context.Context, ids []string) ([]T, error) {
	if len(ids) == 0 {
		return make([]T, 0), nil
	}
	return q.WhereIn("?TableAlias.id", ids).Get(ctx)
}

// FirstWhere 查找指定列满足条件的首条记录（对标 Laravel firstWhere）。
// 支持 1 个参数（即 column = val）或 2 个以上参数（即 column op val）。
func (q *Query[T]) FirstWhere(ctx context.Context, column string, args ...any) (*T, error) {
	switch len(args) {
	case 1:
		return q.Where(column+" = ?", args[0]).First(ctx)
	case 0:
		return q.Where(column).First(ctx)
	default:
		return q.Where(column+" "+fmt.Sprint(args[0])+" ?", args[1:]...).First(ctx)
	}
}

// Get 执行查询返回切片列表。
func (q *Query[T]) Get(ctx context.Context) ([]T, error) {
	var items []T
	sel := q.BuildSelect()
	if err := sel.Scan(ctx, &items); err != nil {
		return nil, err
	}
	if items == nil {
		items = make([]T, 0)
	}
	for i := range items {
		if dt, ok := any(&items[i]).(DirtyTrackable); ok {
			dt.SyncOriginal()
		}
	}
	return items, nil
}

// Count 统计匹配条件的记录数。
func (q *Query[T]) Count(ctx context.Context) (int, error) {
	sel := q.BuildSelect()
	count, err := sel.Count(ctx)
	return int(count), err
}

// Exists 判定是否存在满足条件的记录。
func (q *Query[T]) Exists(ctx context.Context) (bool, error) {
	sel := q.BuildSelect()
	return sel.Exists(ctx)
}

// Aggregate 执行通用聚合表达式（如 "SUM(amount)", "AVG(score)"）并将结果扫描至 dest，自动继承软删除过滤。
func (q *Query[T]) Aggregate(ctx context.Context, expr string, dest any) error {
	sel := q.BuildSelect()
	return sel.ColumnExpr(expr).Scan(ctx, dest)
}

func (q *Query[T]) floatAggregate(ctx context.Context, expr string) (float64, error) {
	var val float64
	err := q.Aggregate(ctx, expr, &val)
	return val, err
}

// Sum 统计数值列总和（自动继承软删除过滤，空结果返回 0）。
func (q *Query[T]) Sum(ctx context.Context, column string) (float64, error) {
	return q.floatAggregate(ctx, "COALESCE(SUM("+column+"), 0)")
}

// Avg 计算数值列平均值（自动继承软删除过滤，空结果返回 0）。
func (q *Query[T]) Avg(ctx context.Context, column string) (float64, error) {
	return q.floatAggregate(ctx, "COALESCE(AVG("+column+"), 0)")
}

// Min 查找数值列最小值（自动继承软删除过滤）。
func (q *Query[T]) Min(ctx context.Context, column string) (float64, error) {
	return q.floatAggregate(ctx, "MIN("+column+")")
}

// Max 查找数值列最大值（自动继承软删除过滤）。
func (q *Query[T]) Max(ctx context.Context, column string) (float64, error) {
	return q.floatAggregate(ctx, "MAX("+column+")")
}

// GroupCountItem 承载分组统计扫描的键值行。
type GroupCountItem struct {
	Key   string `bun:"key"`
	Count int64  `bun:"count"`
}

// CountBy 针对指定字段执行分组计数统计，并在单条 SQL 中返回映射字典 map[string]int64（对标 Laravel countBy）。
// 彻底规避拉取全量数据后在应用内存中手动计数的低效反模式。
func (q *Query[T]) CountBy(ctx context.Context, column string) (map[string]int64, error) {
	var rows []GroupCountItem
	sel := q.BuildSelect().
		ColumnExpr("?::text AS key", bun.Ident(column)).
		ColumnExpr("COUNT(*) AS count").
		GroupExpr("?", bun.Ident(column))

	if err := sel.Scan(ctx, &rows); err != nil {
		return nil, err
	}
	result := make(map[string]int64, len(rows))
	for _, r := range rows {
		result[r.Key] = r.Count
	}
	return result, nil
}

// GroupSumItem 承载分组求和扫描的键值行。
type GroupSumItem struct {
	Key   string  `bun:"key"`
	Total float64 `bun:"total"`
}

// SumBy 针对指定字段执行分组求和，并在单条 SQL 中返回映射字典 map[string]float64。
func (q *Query[T]) SumBy(ctx context.Context, groupColumn, sumColumn string) (map[string]float64, error) {
	var rows []GroupSumItem
	sel := q.BuildSelect().
		ColumnExpr("?::text AS key", bun.Ident(groupColumn)).
		ColumnExpr("COALESCE(SUM(?), 0) AS total", bun.Ident(sumColumn)).
		GroupExpr("?", bun.Ident(groupColumn))

	if err := sel.Scan(ctx, &rows); err != nil {
		return nil, err
	}
	result := make(map[string]float64, len(rows))
	for _, r := range rows {
		result[r.Key] = r.Total
	}
	return result, nil
}

// KeyValueItem 承载单查询键值对抽取。
type KeyValueItem struct {
	Key   string `bun:"key"`
	Value any    `bun:"value"`
}

// PluckMap 在单条 SQL 查询中将两列数据直接提取为键值映射字典 map[string]any。
func (q *Query[T]) PluckMap(ctx context.Context, keyColumn, valueColumn string) (map[string]any, error) {
	var rows []KeyValueItem
	sel := q.BuildSelect().
		ColumnExpr("?::text AS key", bun.Ident(keyColumn)).
		ColumnExpr("? AS value", bun.Ident(valueColumn))

	if err := sel.Scan(ctx, &rows); err != nil {
		return nil, err
	}
	result := make(map[string]any, len(rows))
	for _, r := range rows {
		result[r.Key] = r.Value
	}
	return result, nil
}

// Value 提取首条匹配记录的单列标量值（未找到时返回 sql.ErrNoRows）。
func (q *Query[T]) Value(ctx context.Context, column string) (any, error) {
	var val any
	sel := q.BuildSelect()
	if err := sel.Column(column).Limit(1).Scan(ctx, &val); err != nil {
		return nil, err
	}
	return val, nil
}

// Pluck 从查询器中提取指定单列数据为任意类型的泛型切片 []V（如 []uuid.UUID, []int64, []string）。
func Pluck[V any, T any](ctx context.Context, q *Query[T], column string) ([]V, error) {
	var results []V
	sel := q.BuildSelect()
	if err := sel.Column(column).Scan(ctx, &results); err != nil {
		return nil, err
	}
	if results == nil {
		results = make([]V, 0)
	}
	return results, nil
}

// PluckStrings 提取指定单列数据为 []string 切片（常用于批量获取 ID 或标识符）。
func (q *Query[T]) PluckStrings(ctx context.Context, column string) ([]string, error) {
	return Pluck[string](ctx, q, column)
}
