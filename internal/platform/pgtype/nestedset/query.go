package nestedset

import (
	"github.com/uptrace/bun"
)

// WhereDescendantOf 向 Bun 查询追加“为目标路径之后代或自身”（<@ 算子）的条件。
// 例如查询某个部门及其所有下属团队。
func WhereDescendantOf(q *bun.SelectQuery, column string, parent Path) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	return q.Where("?::ltree <@ ?::ltree", bun.Ident(column), parent)
}

// WhereAncestorOf 向 Bun 查询追加“为目标路径之祖先或自身”（@> 算子）的条件。
// 例如向上回溯查询当前节点的所有父层节点，用于构建面包屑导航 (Breadcrumbs)。
func WhereAncestorOf(q *bun.SelectQuery, column string, child Path) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	return q.Where("?::ltree @> ?::ltree", bun.Ident(column), child)
}

// WhereDirectChildren 向 Bun 查询追加“仅查询直接下属第一层子节点”的条件。
func WhereDirectChildren(q *bun.SelectQuery, column string, parent Path) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	return q.Where("?::ltree <@ ?::ltree AND nlevel(?::ltree) = nlevel(?::ltree) + 1",
		bun.Ident(column), parent, bun.Ident(column), parent)
}

// WhereMatchLQuery 向 Bun 查询追加基于 PostgreSQL lquery 正则表达式的匹配条件（~ 算子）。
// 例如 pattern 为 "*.Science.*" 或 "Top.*{1,2}"。
func WhereMatchLQuery(q *bun.SelectQuery, column string, pattern string) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	return q.Where("?::ltree ~ ?::lquery", bun.Ident(column), pattern)
}

// OrderByPath 向 Bun 查询追加按 ltree 字典序排序，保证树形结构呈前序遍历深度优先展示。
func OrderByPath(q *bun.SelectQuery, column string, asc bool) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	dir := "ASC"
	if !asc {
		dir = "DESC"
	}
	return q.OrderExpr("?::ltree "+dir, bun.Ident(column))
}

// OrderByDepth 向 Bun 查询追加按节点在树中的层级深度（nlevel）排序。
func OrderByDepth(q *bun.SelectQuery, column string, asc bool) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	dir := "ASC"
	if !asc {
		dir = "DESC"
	}
	return q.OrderExpr("nlevel(?::ltree) "+dir, bun.Ident(column))
}

// ApplyMoveSubtree 向 Bun UpdateQuery 注入整棵子树原子迁移挂载的 SET 与 WHERE 表达式。
// 利用 PostgreSQL ltree 原生 subpath 函数将所有旧前缀子孙节点原子改写为新前缀：
func ApplyMoveSubtree(q *bun.UpdateQuery, column string, oldPrefix, newPrefix Path) *bun.UpdateQuery {
	if q == nil {
		return nil
	}
	setExpr := "? = ?::ltree || subpath(?, nlevel(?::ltree))"
	whereExpr := "?::ltree <@ ?::ltree"
	return q.Set(setExpr, bun.Ident(column), newPrefix, bun.Ident(column), oldPrefix).
		Where(whereExpr, bun.Ident(column), oldPrefix)
}
