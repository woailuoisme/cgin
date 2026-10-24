package database_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/database"
	"bgin/internal/database/models"
)

// TestActiveRecord_Execution_ChunkAndCursor 测试基于分页与原生 Rows 游标的流式遍历。
func TestActiveRecord_Execution_ChunkAndCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMockDB(t)

	t.Run("Chunk iterates until page has fewer than chunkSize items", func(t *testing.T) {
		pageCall := 0
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			pageCall++
			if pageCall == 1 {
				return makeMockRows(
					[]string{"id", "email"},
					[]driver.Value{"u-1", "u1@example.com"},
					[]driver.Value{"u-2", "u2@example.com"},
				), nil
			}
			return makeMockRows([]string{"id", "email"}), nil // empty ends chunk
		})

		var collected []string
		q := database.NewQuery[models.User](db)
		err := q.Chunk(ctx, 2, func(items []models.User) error {
			for _, item := range items {
				collected = append(collected, item.ID)
			}
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"u-1", "u-2"}, collected)
	})

	t.Run("Chunk returns error if callback fails", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email"},
				[]driver.Value{"u-1", "u1@example.com"},
			), nil
		})

		q := database.NewQuery[models.User](db)
		err := q.Chunk(ctx, 5, func(items []models.User) error {
			return errors.New("chunk callback aborted")
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "chunk callback aborted")
	})

	t.Run("Cursor streams rows via ScanRow with O(1) memory and syncs dirty baseline", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "title"},
				[]driver.Value{"cursor-1", "T1"},
				[]driver.Value{"cursor-2", "T2"},
			), nil
		})

		var streamedIDs []string
		q := database.NewQuery[mockTrackableItem](db)
		err := q.Cursor(ctx, func(item *mockTrackableItem) error {
			streamedIDs = append(streamedIDs, item.ID)
			assert.False(t, item.IsDirty(), "Cursor streamed item should automatically have clean dirty baseline")
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"cursor-1", "cursor-2"}, streamedIDs)
	})

	t.Run("Cursor aborts iteration immediately when callback returns error", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"cursor-1", "c1@example.com", "C1", "user", "active"},
				[]driver.Value{"cursor-2", "c2@example.com", "C2", "user", "active"},
			), nil
		})

		q := database.NewQuery[models.User](db)
		err := q.Cursor(ctx, func(user *models.User) error {
			return errors.New("stop stream")
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stop stream")
	})

	t.Run("Lazy yields items via iter.Seq2 for-range with dirty baseline synced", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "title"},
				[]driver.Value{"lazy-1", "L1"},
				[]driver.Value{"lazy-2", "L2"},
			), nil
		})

		var lazyIDs []string
		q := database.NewQuery[mockTrackableItem](db)
		for item, err := range q.Lazy(ctx) {
			require.NoError(t, err)
			lazyIDs = append(lazyIDs, item.ID)
			assert.False(t, item.IsDirty(), "Lazy item should automatically sync dirty baseline")
		}
		assert.Equal(t, []string{"lazy-1", "lazy-2"}, lazyIDs)
	})
}
