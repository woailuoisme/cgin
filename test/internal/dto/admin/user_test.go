// Package admindto_test 验证后台管理域对外暴露的纯逻辑与契约。
package admindto_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	admindto "cgin/internal/dto/admin"
	"cgin/internal/user"
)

// api.md 6.1 规定的分页默认值与上限。
const (
	wantDefaultPage     = 1
	wantDefaultPageSize = 20
	wantMaxPageSize     = 100
	wantMaxPage         = 1_000_000
)

// TestPaginationContract 校验 api.md 6.1 的分页契约（默认值、上限与越界收敛）由服务层真实承载。
func TestPaginationContract(t *testing.T) {
	// 省略全部字段：收敛到默认值。
	page, pageSize := admindto.NormalisePagination(0, 0)
	assert.Equal(t, wantDefaultPage, page)
	assert.Equal(t, wantDefaultPageSize, pageSize)

	// 越界取值：page 抬到下限，pageSize 压到上限。
	page, pageSize = admindto.NormalisePagination(-5, 1000)
	assert.Equal(t, wantDefaultPage, page)
	assert.Equal(t, wantMaxPageSize, pageSize)

	// 超大页码必须收敛，否则 (page-1)*pageSize 会溢出为负值。
	page, _ = admindto.NormalisePagination(math.MaxInt64, wantDefaultPageSize)
	assert.Equal(t, wantMaxPage, page)

	// 正常取值原样透传。
	page, pageSize = admindto.NormalisePagination(3, 50)
	assert.Equal(t, 3, page)
	assert.Equal(t, 50, pageSize)

	// 请求结构体允许省略分页字段，交由服务层收敛。
	var query admindto.ListUsersQueryParams
	require.NoError(t, json.Unmarshal([]byte(`{}`), &query))
	assert.Zero(t, query.Page)
	assert.Zero(t, query.PageSize)
}

// TestListUsersQueryAcceptsCompoundSort 校验查询参数结构体允许复合排序表达式。
func TestListUsersQueryAcceptsCompoundSort(t *testing.T) {
	var query admindto.ListUsersQueryParams
	require.NoError(t, json.Unmarshal([]byte(`{"sort":"-createdAt,email"}`), &query))
	assert.Equal(t, "-createdAt,email", query.Sort)
	key, desc := user.NormaliseSort(query.Sort)
	assert.Equal(t, "createdAt", key)
	assert.True(t, desc)
}
