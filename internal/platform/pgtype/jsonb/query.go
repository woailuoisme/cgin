package jsonb

import (
	"encoding/json"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// toJSONString 将任意对象转换为 JSON 字符串；若为 string 或 []byte 则直接转换为 string。
func toJSONString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []byte:
		return string(val)
	default:
		bytes, err := json.Marshal(v)
		if err != nil {
			return "{}"
		}
		return string(bytes)
	}
}

// WhereContains 向 Bun 查询追加 JSONB 包含条件（@> 算子，支持 GIN 索引快速检索）。
// 例如查询 metadata 中包含 {"env": "prod", "tier": 1} 的行。
func WhereContains(q *bun.SelectQuery, column string, subset any) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	return q.Where("?::jsonb @> ?::jsonb", bun.Ident(column), toJSONString(subset))
}

// WhereContained 向 Bun 查询追加 JSONB 被包含条件（<@ 算子）。
func WhereContained(q *bun.SelectQuery, column string, superset any) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	return q.Where("?::jsonb <@ ?::jsonb", bun.Ident(column), toJSONString(superset))
}

// WhereHasKey 向 Bun 查询追加顶层键是否存在条件（PostgreSQL ? 算子 / jsonb_exists 函数）。
func WhereHasKey(q *bun.SelectQuery, column string, key string) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	return q.Where("jsonb_exists(?::jsonb, ?)", bun.Ident(column), key)
}

// WhereHasAnyKeys 向 Bun 查询追加“存在任意一个候选键”（PostgreSQL ?| 算子 / jsonb_exists_any 函数）的条件。
func WhereHasAnyKeys(q *bun.SelectQuery, column string, keys ...string) *bun.SelectQuery {
	if q == nil || len(keys) == 0 {
		return q
	}
	return q.Where("jsonb_exists_any(?::jsonb, ?)", bun.Ident(column), pgdialect.Array(keys))
}

// WhereHasAllKeys 向 Bun 查询追加“同时存在所有指定键”（PostgreSQL ?& 算子 / jsonb_exists_all 函数）的条件。
func WhereHasAllKeys(q *bun.SelectQuery, column string, keys ...string) *bun.SelectQuery {
	if q == nil || len(keys) == 0 {
		return q
	}
	return q.Where("jsonb_exists_all(?::jsonb, ?)", bun.Ident(column), pgdialect.Array(keys))
}

// WhereExtractTextEq 向 Bun 查询追加按一级字段提取文本并匹配相等的条件（->> 算子）。
func WhereExtractTextEq(q *bun.SelectQuery, column string, field string, expected any) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	return q.Where("(? ->> ?) = ?", bun.Ident(column), field, expected)
}

// WherePathTextEq 向 Bun 查询追加按多级路径深层提取文本并匹配相等的条件（#>> 算子）。
func WherePathTextEq(q *bun.SelectQuery, column string, path []string, expected any) *bun.SelectQuery {
	if q == nil || len(path) == 0 {
		return q
	}
	return q.Where("(? #>> ?) = ?", bun.Ident(column), pgdialect.Array(path), expected)
}

// ColumnExtractText 在 Bun 查询中将指定 JSONB 一级字段提取为文本并设置列别名（->> 算子）。
func ColumnExtractText(q *bun.SelectQuery, column string, field string, alias string) *bun.SelectQuery {
	if q == nil {
		return nil
	}
	return q.ColumnExpr("(? ->> ?) AS ?", bun.Ident(column), field, bun.Ident(alias))
}

// ColumnPathText 在 Bun 查询中按多级路径提取深层文本并设置列别名（#>> 算子）。
func ColumnPathText(q *bun.SelectQuery, column string, path []string, alias string) *bun.SelectQuery {
	if q == nil || len(path) == 0 {
		return q
	}
	return q.ColumnExpr("(? #>> ?) AS ?", bun.Ident(column), pgdialect.Array(path), bun.Ident(alias))
}

// ApplyMerge 在 Bun UpdateQuery 中原子合并两个 JSONB 对象（|| 算子）。
func ApplyMerge(q *bun.UpdateQuery, column string, patch any) *bun.UpdateQuery {
	if q == nil {
		return nil
	}
	return q.Set("? = ?::jsonb || ?::jsonb", bun.Ident(column), bun.Ident(column), toJSONString(patch))
}

// ApplyRemoveKey 在 Bun UpdateQuery 中从 JSONB 顶层原子移除指定字段（- 算子）。
func ApplyRemoveKey(q *bun.UpdateQuery, column string, key string) *bun.UpdateQuery {
	if q == nil {
		return nil
	}
	return q.Set("? = ?::jsonb - ?", bun.Ident(column), bun.Ident(column), key)
}

// ApplyRemovePath 在 Bun UpdateQuery 中从 JSONB 深层原子移除指定路径元素（#- 算子）。
func ApplyRemovePath(q *bun.UpdateQuery, column string, path []string) *bun.UpdateQuery {
	if q == nil || len(path) == 0 {
		return q
	}
	return q.Set("? = ?::jsonb #- ?", bun.Ident(column), bun.Ident(column), pgdialect.Array(path))
}
