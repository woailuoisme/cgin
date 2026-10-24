package geo

import (
	"github.com/uptrace/bun"
)

// SQLMakePoint 生成构造带 SRID 的 PostGIS 点几何对象的 SQL 表达式与参数。
func SQLMakePoint(p Point) (string, []any) {
	srid := NormalizeSRID(p.SRID)
	return "ST_SetSRID(ST_MakePoint(?, ?), ?)", []any{p.Lng(), p.Lat(), srid}
}

// SQLDWithin 生成基于地球真实椭球体（米级单位）的半径范围过滤 SQL 表达式与参数。
func SQLDWithin(column string, center Point, distanceMeters float64) (string, []any) {
	srid := NormalizeSRID(center.SRID)
	sql := "ST_DWithin(?::geography, ST_SetSRID(ST_MakePoint(?, ?), ?)::geography, ?)"
	return sql, []any{bun.Ident(column), center.Lng(), center.Lat(), srid, distanceMeters}
}

// SQLContains 生成判定多边形列是否包含指定点的 SQL 表达式与参数。
func SQLContains(column string, p Point) (string, []any) {
	srid := NormalizeSRID(p.SRID)
	sql := "ST_Contains(?::geometry, ST_SetSRID(ST_MakePoint(?, ?), ?))"
	return sql, []any{bun.Ident(column), p.Lng(), p.Lat(), srid}
}

// SQLDistance 生成计算指定空间列与参考点之间的地表距离（单位：米）的 SQL 表达式与参数。
func SQLDistance(column string, center Point) (string, []any) {
	srid := NormalizeSRID(center.SRID)
	sql := "ST_Distance(?::geography, ST_SetSRID(ST_MakePoint(?, ?), ?)::geography)"
	return sql, []any{bun.Ident(column), center.Lng(), center.Lat(), srid}
}

// WhereDWithin 向 Bun 查询构建器追加基于大圆米级距离的范围过滤条件（ST_DWithin）。
func WhereDWithin(q *bun.SelectQuery, column string, center Point, distanceMeters float64) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	expr, args := SQLDWithin(column, center, distanceMeters)
	return q.Where(expr, args...)
}

// WhereContains 向 Bun 查询构建器追加点在多边形内部检测条件（ST_Contains）。
func WhereContains(q *bun.SelectQuery, column string, p Point) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	expr, args := SQLContains(column, p)
	return q.Where(expr, args...)
}

// WhereWithinBox 向 Bun 查询构建器追加在指定经纬度外接矩形范围内的过滤条件（ST_MakeEnvelope）。
func WhereWithinBox(q *bun.SelectQuery, column string, minLng, minLat, maxLng, maxLat float64, sridOpt ...int) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	srid := DefaultSRID
	if len(sridOpt) > 0 {
		srid = NormalizeSRID(sridOpt[0])
	}
	sql := "ST_Within(?::geometry, ST_MakeEnvelope(?, ?, ?, ?, ?))"
	return q.Where(sql, bun.Ident(column), minLng, minLat, maxLng, maxLat, srid)
}

// WhereIntersects 向 Bun 查询构建器追加两空间几何相交的检测条件（ST_Intersects）。
func WhereIntersects(q *bun.SelectQuery, column string, geom any) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	sql := "ST_Intersects(?::geometry, ?)"
	return q.Where(sql, bun.Ident(column), geom)
}

// OrderByDistance 向 Bun 查询构建器追加按与参考点的地表米级距离排序。
func OrderByDistance(q *bun.SelectQuery, column string, center Point, asc bool) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	expr, args := SQLDistance(column, center)
	order := "ASC"
	if !asc {
		order = "DESC"
	}
	return q.OrderExpr(expr+" "+order, args...)
}

// OrderByDistanceAsc 向 Bun 查询构建器追加按与参考点的地表米级距离升序排序。
func OrderByDistanceAsc(q *bun.SelectQuery, column string, center Point) *bun.SelectQuery {
	return OrderByDistance(q, column, center, true)
}

// OrderByDistanceDesc 向 Bun 查询构建器追加按与参考点的地表米级距离降序排序。
func OrderByDistanceDesc(q *bun.SelectQuery, column string, center Point) *bun.SelectQuery {
	return OrderByDistance(q, column, center, false)
}

// ColumnDistance 在 Bun 查询的投影列中追加与参考点的地表大圆距离（单位：米），重命名为 alias。
func ColumnDistance(q *bun.SelectQuery, column string, center Point, alias string) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	expr, args := SQLDistance(column, center)
	args = append(args, bun.Ident(alias))
	return q.ColumnExpr(expr+" AS ?", args...)
}
