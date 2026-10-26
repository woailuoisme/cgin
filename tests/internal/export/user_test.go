package export_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/database/sqlc"
	"cgin/internal/enum"
	"cgin/internal/export"
	"cgin/internal/import"
	"cgin/internal/user"
	"cgin/pkg/errs"
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
	err := export.Users(
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

	var imported []importer.UserRow
	err = importer.Users(
		t.Context(),
		bytes.NewReader(buf.Bytes()),
		func(_ context.Context, rows []importer.UserRow) error {
			imported = append(imported, rows...)
			return nil
		},
	)
	require.NoError(t, err)
	require.Len(t, imported, 2)

	assert.Equal(t, importer.UserRow{
		Name:   "Alice Smith",
		Email:  "alice@example.com",
		Role:   enum.RoleAdmin,
		Status: enum.StatusActive,
	}, imported[0])

	assert.Equal(t, importer.UserRow{
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
		err := export.Users(
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

		err = importer.Users(t.Context(), bytes.NewReader(buf.Bytes()), func(context.Context, []importer.UserRow) error {
			return nil
		})
		require.Error(t, err)
		bizErr, ok := errs.As(err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeExcelDuplicateRow, bizErr.Code())
	})
}
