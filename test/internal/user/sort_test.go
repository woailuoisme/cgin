package user_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"cgin/internal/user"
)

// TestNormaliseSortUsesWhitelist 校验排序只认白名单字段，注入尝试被丢弃。
func TestNormaliseSortUsesWhitelist(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantKey  string
		wantDesc bool
	}{
		{"缺省退回稳定排序", "", "createdAt", true},
		{"升序", "email", "email", false},
		{"降序", "-name", "name", true},
		{"复合排序取首个合法键", "createdAt,-email", "createdAt", false},
		{"注入尝试被丢弃", `id; DROP TABLE "user"`, "createdAt", true},
		{"未知字段与空白被剥掉", " unknown , email ,", "email", false},
		{"不在白名单的列被忽略", "u.password", "createdAt", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, desc := user.NormaliseSort(tt.raw)
			assert.Equal(t, tt.wantKey, key)
			assert.Equal(t, tt.wantDesc, desc)
		})
	}
}
