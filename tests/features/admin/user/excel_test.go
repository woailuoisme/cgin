package admin_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"bgin/internal/database"
	"bgin/internal/database/models"
	admin "bgin/internal/features/admin/user"
	"bgin/internal/shared/user"
)

func TestUsersExportContract(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	users := []*models.User{
		{
			Base:   database.Base{ID: "usr_001", CreatedAt: now},
			Name:   "Alice",
			Email:  "alice@example.com",
			Role:   models.RoleAdmin,
			Status: models.StatusActive,
		},
	}

	exp := admin.NewUsersExport(users)

	assert.Equal(t, "Users", exp.Title())
	assert.Equal(t, []string{"ID", "Name", "Email", "Role", "Status", "Created At"}, exp.Headings())
	assert.Equal(t, users, exp.FromSlice())

	row := exp.Mapping(users[0])
	require.Len(t, row, 6)
	assert.Equal(t, "usr_001", row[0])
	assert.Equal(t, "Alice", row[1])
	assert.Equal(t, "alice@example.com", row[2])
	assert.Equal(t, models.RoleAdmin, row[3])
	assert.Equal(t, models.StatusActive, row[4])
	assert.Equal(t, now.Format(time.RFC3339), row[5])

	// 验证流式导出到内存 buffer
	var buf bytes.Buffer
	err := admin.ExportStream(t.Context(), &buf, users)
	require.NoError(t, err)
	assert.Positive(t, buf.Len())
}

type mockUserFetcher struct {
	users []models.User
	calls int
}

func (m *mockUserFetcher) List(_ context.Context, _ user.ListFilter, offset, limit int) ([]models.User, error) {
	m.calls++
	if offset >= len(m.users) {
		return nil, nil
	}
	end := min(offset+limit, len(m.users))
	return m.users[offset:end], nil
}

func TestUsersChunkedExportContract(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	var dataset []models.User
	for i := 1; i <= 5; i++ {
		dataset = append(dataset, models.User{
			Base:   database.Base{ID: fmt.Sprintf("usr_%03d", i), CreatedAt: now},
			Name:   fmt.Sprintf("User%d", i),
			Email:  fmt.Sprintf("user%d@example.com", i),
			Role:   models.RoleUser,
			Status: models.StatusActive,
		})
	}

	fetcher := &mockUserFetcher{users: dataset}
	chunkSize := 2
	exp := admin.NewUsersChunkedExport(fetcher, user.ListFilter{}, chunkSize)

	assert.Equal(t, "Users", exp.Title())
	assert.Equal(t, []string{"ID", "Name", "Email", "Role", "Status", "Created At"}, exp.Headings())
	assert.Equal(t, 2, exp.ChunkSize())

	// 直接验证 FetchChunk 行为
	chunk, err := exp.FetchChunk(t.Context(), 0, 2)
	require.NoError(t, err)
	require.Len(t, chunk, 2)

	// 验证 Mapping
	row := exp.Mapping(chunk[0])
	require.Len(t, row, 6)
	assert.Equal(t, "usr_001", row[0])
	assert.Equal(t, "User1", row[1])
	assert.Equal(t, "user1@example.com", row[2])

	// 验证分块流式导出全量生成真实 XLSX 文件
	var buf bytes.Buffer
	err = admin.ExportChunkedStream(t.Context(), &buf, fetcher, user.ListFilter{})
	require.NoError(t, err)
	assert.Positive(t, buf.Len())

	// 解码生成的 Excel 文件验证完整性
	f, err := excelize.OpenReader(&buf)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	rows, err := f.GetRows("Users")
	require.NoError(t, err)
	// 表头 1 行 + 数据 5 行 = 6 行
	require.Len(t, rows, 6)
	assert.Equal(t, []string{"ID", "Name", "Email", "Role", "Status", "Created At"}, rows[0])
	assert.Equal(t, "usr_001", rows[1][0])
	assert.Equal(t, "User1", rows[1][1])
	assert.Equal(t, "usr_005", rows[5][0])
	assert.Equal(t, "User5", rows[5][1])
}

func TestUsersImportContract(t *testing.T) {
	t.Parallel()

	imp := admin.NewUsersImport()
	assert.Equal(t, 1, imp.HeadingRow())
	assert.Equal(t, 100, imp.BatchSize())
	assert.Equal(t, 0, imp.ImportedCount())

	// 测试数据列数不足
	_, err := imp.ToModel([]string{"OnlyName"})
	require.Error(t, err)

	// 测试空数据校验失败
	_, err = imp.ToModel([]string{"", "test@example.com"})
	require.Error(t, err)

	// 测试正常行解析映射
	modelAny, err := imp.ToModel([]string{"Bob Builder", "bob@example.com"})
	require.NoError(t, err)

	u, ok := modelAny.(*models.User)
	require.True(t, ok)
	assert.Equal(t, "Bob Builder", u.Name)
	assert.Equal(t, "bob@example.com", u.Email)
	assert.Equal(t, models.RoleUser, u.Role)
	assert.Equal(t, 1, imp.ImportedCount())
}
