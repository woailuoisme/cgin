package user_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/database/sqlc"
	"cgin/internal/enum"
	adminuser "cgin/internal/feature/admin/user"
	"cgin/internal/user"
	"cgin/pkg/errs"
	"cgin/pkg/excel"
)

func TestUserExportAndImportRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	sourceUsers := []sqlc.User{
		{
			ID:        "u_1",
			Name:      "Alice Smith",
			Email:     "alice@example.com",
			Role:      enum.RoleAdmin,
			Status:    enum.StatusActive,
			CreatedAt: now,
		},
		{
			ID:        "u_2",
			Name:      "Bob Jones",
			Email:     "bob@example.com",
			Role:      enum.RoleUser,
			Status:    enum.StatusSuspended,
			CreatedAt: now,
		},
	}

	var buf bytes.Buffer
	err := adminuser.ExportUsers(
		t.Context(),
		&buf,
		func(_ context.Context, _ user.ListFilter, afterID string, _ int) ([]sqlc.User, error) {
			if afterID != "" {
				return nil, nil
			}
			return sourceUsers, nil
		},
		user.ListFilter{},
	)
	require.NoError(t, err)
	require.NotEmpty(t, buf.Bytes())

	var imported []adminuser.UserRow
	result, err := adminuser.ImportUsers(
		t.Context(),
		bytes.NewReader(buf.Bytes()),
		func(_ context.Context, rows []adminuser.UserRow) error {
			imported = append(imported, rows...)
			return nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, 2, result.TotalRows)
	assert.Equal(t, 2, result.SuccessRows)
	assert.Equal(t, 0, result.FailedRows)
	assert.False(t, result.HasErrors())
	require.Len(t, imported, 2)

	assert.Equal(t, adminuser.UserRow{
		Name:   "Alice Smith",
		Email:  "alice@example.com",
		Role:   enum.RoleAdmin,
		Status: enum.StatusActive,
	}, imported[0])

	assert.Equal(t, adminuser.UserRow{
		Name:   "Bob Jones",
		Email:  "bob@example.com",
		Role:   enum.RoleUser,
		Status: enum.StatusSuspended,
	}, imported[1])
}

func TestUserImport_DefaultRoleStatusAndValidation(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	t.Run("重复邮箱触发 EXCEL_DUPLICATE_ROW", func(t *testing.T) {
		var buf bytes.Buffer
		err := adminuser.ExportUsers(
			t.Context(),
			&buf,
			func(_ context.Context, _ user.ListFilter, afterID string, _ int) ([]sqlc.User, error) {
				if afterID != "" {
					return nil, nil
				}
				return []sqlc.User{
					{ID: "1", Name: "Alice", Email: "dup@example.com", Role: enum.RoleUser, Status: enum.StatusActive, CreatedAt: now},
					{ID: "2", Name: "Alice 2", Email: "DUP@example.com", Role: enum.RoleUser, Status: enum.StatusActive, CreatedAt: now},
				}, nil
			},
			user.ListFilter{},
		)
		require.NoError(t, err)

		_, err = adminuser.ImportUsers(t.Context(), bytes.NewReader(buf.Bytes()), func(context.Context, []adminuser.UserRow) error {
			return nil
		})
		require.Error(t, err)
		bizErr, ok := errs.As(err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeExcelDuplicateRow, bizErr.Code())
	})

	t.Run("多字节中文姓名按字符数校验（50汉字合法、101汉字超限）", func(t *testing.T) {
		validChineseName := strings.Repeat("张", 50) // 150 UTF-8 字节，但仅 50 个 Unicode 字符
		tooLongChineseName := strings.Repeat("王", 101)

		var buf bytes.Buffer
		require.NoError(t, adminuser.ExportUsers(
			t.Context(),
			&buf,
			func(_ context.Context, _ user.ListFilter, afterID string, _ int) ([]sqlc.User, error) {
				if afterID != "" {
					return nil, nil
				}
				return []sqlc.User{
					{ID: "1", Name: validChineseName, Email: "cn1@example.com", Role: enum.RoleUser, Status: enum.StatusActive, CreatedAt: now},
					{ID: "2", Name: tooLongChineseName, Email: "cn2@example.com", Role: enum.RoleUser, Status: enum.StatusActive, CreatedAt: now},
				}, nil
			},
			user.ListFilter{},
		))

		var imported []adminuser.UserRow
		result, err := adminuser.ImportUsers(
			t.Context(),
			bytes.NewReader(buf.Bytes()),
			func(_ context.Context, rows []adminuser.UserRow) error {
				imported = append(imported, rows...)
				return nil
			},
			excel.WithSkipInvalid[adminuser.UserRow](true),
		)
		require.NoError(t, err)
		assert.Equal(t, 2, result.TotalRows)
		assert.Equal(t, 1, result.SuccessRows)
		assert.Equal(t, 1, result.FailedRows)
		require.Len(t, imported, 1)
		assert.Equal(t, validChineseName, imported[0].Name)
	})
}
