package user

import (
	"cmp"
	"strings"

	govalidator "github.com/go-playground/validator/v10"
	"github.com/samber/lo"

	"bgin/internal/database/models"
	"bgin/internal/platform/validator"
)

func init() {
	if err := validator.RegisterRule("usersort", ValidateSort, "oneof"); err != nil {
		panic(err)
	}
}

// ListFilter 是列表查询过滤条件的别名，统一定义于 internal/database/models。
type ListFilter = models.UserListFilter

// sortColumns 是允许排序的字段白名单。
var sortColumns = models.UserSortColumns

// writableColumns 是允许写入的列白名单别名。
var writableColumns = models.UserWritableColumns

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
		part = strings.TrimPrefix(part, "-")
		if _, ok := sortColumns[part]; !ok {
			return false
		}
	}
	return true
}

// defaultSort 保证分页遍历的确定性顺序，对应 api.md 6.2。
const defaultSort = "-createdAt"

// NormaliseSort 把 api.md 6.2 的排序表达式翻译成白名单内的列与方向。
// 缺省或全部非法时退回默认排序，保证分页顺序稳定。
func NormaliseSort(raw string) []string {
	raw = cmp.Or(strings.TrimSpace(raw), defaultSort)

	orders := make([]string, 0, 4)
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		descending := strings.HasPrefix(part, "-")
		column, ok := sortColumns[strings.TrimPrefix(part, "-")]
		if !ok {
			continue
		}
		orders = append(orders, column+lo.Ternary(descending, " DESC", " ASC"))
	}

	if len(orders) == 0 {
		orders = append(orders, sortColumns["createdAt"]+" DESC")
	}
	return orders
}
