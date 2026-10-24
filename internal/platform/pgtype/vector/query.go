package vector

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

// Metric 定义 pgvector 支持的向量距离度量类型。
type Metric string

const (
	// MetricCosine 余弦距离度量（算子：<=>），适合未归一化文本语义向量。
	MetricCosine Metric = "<=>"
	// MetricL2 欧几里得距离度量（算子：<->），适合几何空间与通用图像向量。
	MetricL2 Metric = "<->"
	// MetricInnerProduct 负内积度量（算子：<#>），适合归一化后的最大内积检索 (MIPS)。
	MetricInnerProduct Metric = "<#>"
	// MetricL1 曼哈顿距离度量（算子：<+>）。
	MetricL1 Metric = "<+>"
)

// Operator 返回度量在 PostgreSQL 中的 SQL 算子。
func (m Metric) Operator() string {
	switch m {
	case MetricCosine, MetricL2, MetricInnerProduct, MetricL1:
		return string(m)
	default:
		return string(MetricCosine)
	}
}

// SQLDistance 生成计算向量列与目标向量之间距离的 SQL 表达式与参数。
func SQLDistance(column string, queryVec Vector, metric Metric) (string, []any) {
	sql := fmt.Sprintf("(? %s ?)", metric.Operator())
	return sql, []any{bun.Ident(column), queryVec}
}

// OrderByDistance 向 Bun 查询构建器追加按与目标向量的距离排序（用于 Top-K ANN 近邻检索）。
func OrderByDistance(q *bun.SelectQuery, column string, queryVec Vector, metric Metric, asc bool) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	expr, args := SQLDistance(column, queryVec, metric)
	dir := "ASC"
	if !asc {
		dir = "DESC"
	}
	return q.OrderExpr(expr+" "+dir, args...)
}

// OrderByDistanceAsc 向 Bun 查询构建器追加按与目标向量的距离升序排序（用于 Top-K ANN 近邻检索）。
func OrderByDistanceAsc(q *bun.SelectQuery, column string, queryVec Vector, metric Metric) *bun.SelectQuery {
	return OrderByDistance(q, column, queryVec, metric, true)
}

// OrderByDistanceDesc 向 Bun 查询构建器追加按与目标向量的距离降序排序。
func OrderByDistanceDesc(q *bun.SelectQuery, column string, queryVec Vector, metric Metric) *bun.SelectQuery {
	return OrderByDistance(q, column, queryVec, metric, false)
}

// WhereDistanceWithin 向 Bun 查询构建器追加距离阈值截断过滤条件（distance <= maxDistance）。
func WhereDistanceWithin(q *bun.SelectQuery, column string, queryVec Vector, metric Metric, maxDistance float64) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	expr, args := SQLDistance(column, queryVec, metric)
	args = append(args, maxDistance)
	return q.Where(expr+" <= ?", args...)
}

// ColumnDistance 在 Bun 查询的投影列中追加向量距离计算，并指定别名。
func ColumnDistance(q *bun.SelectQuery, column string, queryVec Vector, metric Metric, alias string) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	expr, args := SQLDistance(column, queryVec, metric)
	args = append(args, bun.Ident(alias))
	return q.ColumnExpr(expr+" AS ?", args...)
}

// ColumnSimilarity 在 Bun 查询投影列中将余弦距离自动转化为余弦相似度（1 - (column <=> queryVec)），并指定别名。
func ColumnSimilarity(q *bun.SelectQuery, column string, queryVec Vector, alias string) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	sql := "(1 - (? <=> ?)) AS ?"
	return q.ColumnExpr(sql, bun.Ident(column), queryVec, bun.Ident(alias))
}

// SetHnswEfSearch 设置当前事务或连接的 HNSW 索引查询候选队列大小（值越大召回率越高但更耗时）。
func SetHnswEfSearch(ctx context.Context, db bun.IDB, efSearch int) error {
	if efSearch <= 0 {
		efSearch = 40 // pgvector 默认推荐值
	}
	_, err := db.ExecContext(ctx, "SET LOCAL hnsw.ef_search = ?", efSearch)
	return err
}

// SetIvfflatProbes 设置当前事务或连接的 IVFFlat 索引探查聚类桶数。
func SetIvfflatProbes(ctx context.Context, db bun.IDB, probes int) error {
	if probes <= 0 {
		probes = 1 // pgvector 默认推荐值
	}
	_, err := db.ExecContext(ctx, "SET LOCAL ivfflat.probes = ?", probes)
	return err
}
