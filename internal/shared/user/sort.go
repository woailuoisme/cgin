package user

import (
	"cmp"
	"strings"

	govalidator "github.com/go-playground/validator/v10"

	"cgin/internal/database/sqlc"
	"cgin/internal/platform/validator"
)

func init() {
	if err := validator.RegisterRule("usersort", ValidateSort, "oneof"); err != nil {
		panic(err)
	}
}

// sortColumns 是允许排序的字段白名单。
var sortColumns = map[string]bool{
	"createdAt":   true,
	"updatedAt":   true,
	"email":       true,
	"name":        true,
	"lastLoginAt": true,
}

// defaultSort 保证分页遍历的确定性顺序，对应 api.md 6.2。
const defaultSort = "-createdAt"

// ValidateSort 校验用户列表的排序字段，支持以逗号分隔的复合排序以及 - 降序前缀。
func ValidateSort(field govalidator.FieldLevel) bool {
	raw := field.Field().String()
	if raw == "" {
		return true
	}
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return false
		}
		if !sortColumns[strings.TrimPrefix(part, "-")] {
			return false
		}
	}
	return true
}

// NormaliseSort 把排序表达式归一为单一排序键与方向。
// 多键输入只取首个合法键，随后以 id 倒序兜底，保证翻页顺序稳定。
func NormaliseSort(raw string) (string, bool) {
	raw = cmp.Or(strings.TrimSpace(raw), defaultSort)
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		descending := strings.HasPrefix(part, "-")
		key := strings.TrimPrefix(part, "-")
		if sortColumns[key] {
			return key, descending
		}
	}
	return "createdAt", true
}

// ListParams 生成分页查询参数。
func (f ListFilter) ListParams(page, pageSize int) sqlc.ListUsersParams {
	offset := int64(page-1) * int64(pageSize)
	return sqlc.ListUsersParams(f.ChunkParams(int(offset), pageSize))
}

// ChunkParams 生成分块查询参数（导出与搜索导入共用）。
func (f ListFilter) ChunkParams(offset, limit int) sqlc.ListUsersChunkParams {
	params := sqlc.ListUsersChunkParams{
		SortKey:    f.SortKey,
		SortDesc:   f.SortDesc,
		PageOffset: int64(offset),
		PageLimit:  int64(limit),
	}
	if f.Status != "" {
		status := string(f.Status)
		params.Status = &status
	}
	if f.Role != "" {
		role := string(f.Role)
		params.Role = &role
	}
	if f.Search != "" {
		pattern := "%" + EscapeLike(f.Search) + "%"
		params.Search = &pattern
	}
	return params
}

// EscapeLike 转义 SQL LIKE 特殊通配符，避免用户输入被当作模式语法。
func EscapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
