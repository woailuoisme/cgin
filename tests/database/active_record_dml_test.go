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

// TestActiveRecord_Execution_MutationsAndLifecycle 验证数据变更（增删改查）、脏追踪与模型生命周期。
func TestActiveRecord_Execution_MutationsAndLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMockDB(t)

	t.Run("Increment and Decrement execution", func(t *testing.T) {
		var executedSQL string
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			executedSQL = query
			return mockResult{rowsAffected: 1}, nil
		})

		q := database.NewQuery[models.User](db).Where("id = ?", "u1")
		res, err := q.Increment(ctx, "ban_count")
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Contains(t, executedSQL, "UPDATE")
		assert.Contains(t, executedSQL, `"ban_count" = "ban_count" + 1`)

		res, err = q.Decrement(ctx, "ban_count", 5)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Contains(t, executedSQL, `"ban_count" = "ban_count" - 5`)
	})

	t.Run("Create, CreateMany, Upsert, InsertOrIgnore", func(t *testing.T) {
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return mockResult{rowsAffected: 1}, nil
		})

		q := database.NewQuery[models.User](db)
		user := &models.User{Email: "create@example.com"}
		err := q.Create(ctx, user)
		require.NoError(t, err)
		assert.NotEmpty(t, user.ID) // ID 自动生成

		users := []*models.User{
			{Email: "m1@example.com"},
			{Email: "m2@example.com"},
		}
		err = q.CreateMany(ctx, users)
		require.NoError(t, err)

		err = q.Upsert(ctx, users, []string{"email"}, "status")
		require.NoError(t, err)

		err = q.InsertOrIgnore(ctx, users)
		require.NoError(t, err)
	})

	t.Run("Save inserts when ID is empty, updates when ID is present", func(t *testing.T) {
		var executedSQL string
		db.SetOnAnyQuery(func(query string) {
			executedSQL = query
		})

		q := database.NewQuery[models.User](db)
		newUser := &models.User{Email: "new@example.com"}
		err := q.Save(ctx, newUser)
		require.NoError(t, err)
		assert.Contains(t, executedSQL, "INSERT")

		existingUser := &models.User{Base: database.Base{ID: "existing-id"}, Email: "existing@example.com"}
		err = q.Save(ctx, existingUser)
		require.NoError(t, err)
		assert.Contains(t, executedSQL, "UPDATE")
	})

	t.Run("Save short-circuits when model is clean", func(t *testing.T) {
		execCalled := false
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			execCalled = true
			return mockResult{rowsAffected: 1}, nil
		})

		q := database.NewQuery[mockTrackableItem](db)
		item := &mockTrackableItem{Base: database.Base{ID: "clean-id"}, Title: "Clean"}
		item.SyncOriginal() // 标记为完全无脏字段

		err := q.Save(ctx, item)
		require.NoError(t, err)
		assert.False(t, execCalled, "clean model should short-circuit Save without issuing SQL query")

		// 触发修改后再 Save
		item.Title = "Modified"
		err = q.Save(ctx, item)
		require.NoError(t, err)
		assert.True(t, execCalled, "dirty model should execute UPDATE query")
	})

	t.Run("Delete, DeleteByID, SoftDelete, ForceDelete", func(t *testing.T) {
		var executedSQL string
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			executedSQL = query
			return mockResult{rowsAffected: 1}, nil
		})

		q := database.NewQuery[models.User](db)
		user := &models.User{Base: database.Base{ID: "del-id"}, Email: "del@example.com"}

		// 软删除
		err := q.Delete(ctx, user)
		require.NoError(t, err)
		assert.Contains(t, executedSQL, "UPDATE")
		assert.Contains(t, executedSQL, "deleted_at")

		// 软删除按 ID
		err = q.DeleteByID(ctx, "del-id")
		require.NoError(t, err)
		assert.Contains(t, executedSQL, "deleted_at")

		// 强制物理删除
		err = database.ForceDelete(ctx, db, user)
		require.NoError(t, err)
		assert.Contains(t, executedSQL, "DELETE FROM")
	})

	t.Run("Truncate execution", func(t *testing.T) {
		var executedSQL string
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			executedSQL = query
			return mockResult{rowsAffected: 1}, nil
		})

		q := database.NewQuery[models.User](db)
		err := q.Truncate(ctx)
		require.NoError(t, err)
		assert.Contains(t, executedSQL, "TRUNCATE TABLE")
	})

	t.Run("Touch updates timestamp and issues update", func(t *testing.T) {
		var executedSQL string
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			executedSQL = query
			return mockResult{rowsAffected: 1}, nil
		})

		q := database.NewQuery[models.User](db)
		user := &models.User{Base: database.Base{ID: "touch-id"}}
		err := q.Touch(ctx, user)
		require.NoError(t, err)
		assert.Contains(t, executedSQL, "UPDATE")
		assert.Contains(t, executedSQL, "updated_at")

		err = database.Touch(ctx, db, user)
		require.NoError(t, err)
	})

	t.Run("Refresh reloads model from database", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"ref-id", "refreshed@example.com", "Refreshed Name", "user", "active"},
			), nil
		})

		user := &models.User{Base: database.Base{ID: "ref-id"}, Email: "old@example.com"}
		err := database.Refresh(ctx, db, user)
		require.NoError(t, err)
		assert.Equal(t, "refreshed@example.com", user.Email)
		assert.Equal(t, "Refreshed Name", user.Name)
	})
}

// TestActiveRecord_Execution_FirstOrCreateAndUpdateOrCreate 测试 FirstOrCreate 与 UpdateOrCreate 的存在与不存在分支。
func TestActiveRecord_Execution_FirstOrCreateAndUpdateOrCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMockDB(t)

	t.Run("FirstOrCreate when record already exists", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"existing-id", "exists@example.com", "Existing", "user", "active"},
			), nil
		})

		q := database.NewQuery[models.User](db).Where("email = ?", "exists@example.com")
		result, created, err := q.FirstOrCreate(ctx, &models.User{Email: "exists@example.com"})
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, "existing-id", result.ID)
	})

	t.Run("FirstOrCreate when record does not exist", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"id", "email"}), nil // empty -> ErrNoRows
		})
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return mockResult{rowsAffected: 1}, nil
		})

		q := database.NewQuery[models.User](db).Where("email = ?", "new@example.com")
		newUser := &models.User{Email: "new@example.com"}
		result, created, err := q.FirstOrCreate(ctx, newUser)
		require.NoError(t, err)
		assert.True(t, created)
		assert.NotEmpty(t, result.ID)
	})

	t.Run("FirstOrCreate propagates database query errors", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("db query timeout")
		})

		q := database.NewQuery[models.User](db)
		_, _, err := q.FirstOrCreate(ctx, &models.User{Email: "err@example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db query timeout")
	})

	t.Run("UpdateOrCreate when record exists", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"existing-id", "u@example.com", "Old Name", "user", "active"},
			), nil
		})
		var executedSQL string
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			executedSQL = query
			return mockResult{rowsAffected: 1}, nil
		})

		q := database.NewQuery[models.User](db).Where("email = ?", "u@example.com")
		target := &models.User{Email: "u@example.com", Name: "Updated Name"}
		result, created, err := q.UpdateOrCreate(ctx, target, "name")
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, "existing-id", result.ID)
		assert.Contains(t, executedSQL, "UPDATE")
	})

	t.Run("UpdateOrCreate when record does not exist", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"id", "email"}), nil // empty -> ErrNoRows
		})
		var executedSQL string
		db.SetOnAnyQuery(func(query string) {
			executedSQL = query
		})

		q := database.NewQuery[models.User](db).Where("email = ?", "brandnew@example.com")
		target := &models.User{Email: "brandnew@example.com", Name: "Brand New"}
		result, created, err := q.UpdateOrCreate(ctx, target)
		require.NoError(t, err)
		assert.True(t, created)
		assert.NotEmpty(t, result.ID)
		assert.Contains(t, executedSQL, "INSERT")
	})
}
