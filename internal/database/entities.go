package database

import (
	"time"
)

// UTCPtr 返回时间指针的 UTC 形式，nil 时返回 nil。
func UTCPtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := value.UTC()
	return &converted
}
