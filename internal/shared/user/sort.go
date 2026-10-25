package user

import (
	"cmp"
	"strings"

	govalidator "github.com/go-playground/validator/v10"

	"cgin/internal/database/sqlc"
	"cgin/internal/platform/validator"
)

// ValidationOptions 返回用户模块贡献给校验器的规则。
//
// 规则只在装配根被注册一次，不再通过 init() 自我安装。
func ValidationOptions() []validator.Option {
	return []validator.Option{validator.WithRule("usersort", ValidateSort, "oneof")}
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
	return sqlc.ListUsersParams{
		Status:     optionalText(f.Status),
		Role:       optionalText(f.Role),
		Search:     f.searchPattern(),
		SortKey:    f.SortKey,
		SortDesc:   f.SortDesc,
		PageLimit:  int64(pageSize),
		PageOffset: int64(page-1) * int64(pageSize),
	}
}

// AfterParams 生成键集分块参数：以主键游标推进，不依赖偏移量。
func (f ListFilter) AfterParams(afterID string, limit int) sqlc.ListUsersAfterParams {
	return sqlc.ListUsersAfterParams{
		Status:    optionalText(f.Status),
		Role:      optionalText(f.Role),
		Search:    f.searchPattern(),
		AfterID:   afterID,
		PageLimit: int64(limit),
	}
}

// optionalText 把空字符串枚举收敛为 NULL，交给 sqlc.narg 表达“不过滤”。
func optionalText[T ~string](value T) *string {
	if value == "" {
		return nil
	}
	text := string(value)
	return &text
}

// searchPattern 把关键字包装为 LIKE 模式，已转义通配符。
func (f ListFilter) searchPattern() *string {
	if f.Search == "" {
		return nil
	}
	pattern := "%" + EscapeLike(f.Search) + "%"
	return &pattern
}

// EscapeLike 转义 SQL LIKE 特殊通配符，避免用户输入被当作模式语法。
func EscapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
