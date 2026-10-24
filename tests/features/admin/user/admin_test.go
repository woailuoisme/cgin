// Package admin_test 验证后台管理域对外暴露的纯逻辑：排序白名单与分页契约。
//
// 本包对应后台管理端 API（/docs/admin），这里的测试覆盖核心业务行为。
package admin_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	admin "bgin/internal/features/admin/user"
	"bgin/internal/shared/user"
)

// api.md 6.1 规定的分页默认值与上限。
const (
	wantDefaultPage     = 1
	wantDefaultPageSize = 20
	wantMaxPageSize     = 100
	wantMaxPage         = 1_000_000
)

// TestNormaliseSortUsesWhitelist 校验排序只认白名单字段，注入尝试被丢弃。
func TestNormaliseSortUsesWhitelist(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"缺省退回稳定排序", "", []string{"u.created_at DESC"}},
		{"升序", "email", []string{"u.email ASC"}},
		{"降序", "-name", []string{"u.name DESC"}},
		{"复合排序", "createdAt,-email", []string{"u.created_at ASC", "u.email DESC"}},
		{"注入尝试被丢弃", `id; DROP TABLE "user"`, []string{"u.created_at DESC"}},
		{"未知字段与空白被剥掉", " unknown , email ,", []string{"u.email ASC"}},
		{"不在白名单的列被忽略", "u.password", []string{"u.created_at DESC"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, user.NormaliseSort(tt.raw))
		})
	}
}

// TestPaginationContract 校验 api.md 6.1 的分页契约（默认值、上限与越界收敛）由服务层真实承载。
func TestPaginationContract(t *testing.T) {
	// 省略全部字段：收敛到默认值。
	page, pageSize := admin.NormalisePagination(0, 0)
	assert.Equal(t, wantDefaultPage, page)
	assert.Equal(t, wantDefaultPageSize, pageSize)

	// 越界取值：page 抬到下限，pageSize 压到上限。
	page, pageSize = admin.NormalisePagination(-5, 1000)
	assert.Equal(t, wantDefaultPage, page)
	assert.Equal(t, wantMaxPageSize, pageSize)

	// 超大页码必须收敛，否则 (page-1)*pageSize 会溢出为负值。
	page, _ = admin.NormalisePagination(math.MaxInt64, wantDefaultPageSize)
	assert.Equal(t, wantMaxPage, page)

	// 正常取值原样透传。
	page, pageSize = admin.NormalisePagination(3, 50)
	assert.Equal(t, 3, page)
	assert.Equal(t, 50, pageSize)

	// 请求结构体允许省略分页字段，交由服务层收敛。
	var query admin.ListUsersQuery
	require.NoError(t, json.Unmarshal([]byte(`{}`), &query))
	assert.Zero(t, query.Page)
	assert.Zero(t, query.PageSize)
}

// TestListUsersQueryAcceptsCompoundSort 校验查询参数结构体允许复合排序表达式。
func TestListUsersQueryAcceptsCompoundSort(t *testing.T) {
	var query admin.ListUsersQuery
	require.NoError(t, json.Unmarshal([]byte(`{"sort":"-createdAt,email"}`), &query))
	assert.Equal(t, "-createdAt,email", query.Sort)
	assert.Equal(t, []string{"u.created_at DESC", "u.email ASC"}, user.NormaliseSort(query.Sort))
}
