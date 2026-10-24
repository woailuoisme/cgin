package database

import (
	"time"

	"cgin/internal/database/sqlc"
)

// Media 是 media 表生成类型的语义别名。
//
// sqlc 会按英语单复数把 media 推导为 Medium，业务语义上仍应读作 Media；
// 这里只做零开销类型别名，不包裹、不转发任何字段与方法。
type Media = sqlc.Medium

// UTCPtr 返回时间指针的 UTC 形式，nil 时返回 nil。
func UTCPtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := value.UTC()
	return &converted
}
