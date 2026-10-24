package database_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"bgin/internal/database"
	"bgin/internal/database/models"
)

// TestActiveRecord_Execution_LookupsAndFinders 测试各类查找器与存在性判断在实际 DB 下的执行。
func TestActiveRecord_Execution_LookupsAndFinders(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMockDB(t)

	t.Run("Find and FindOrFail when record exists", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"usr-100", "test@example.com", "Test User", "user", "active"},
			), nil
		})

		q := database.NewQuery[models.User](db)
		user, err := q.Find(ctx, "usr-100")
		require.NoError(t, err)
		require.NotNil(t, user)
		assert.Equal(t, "usr-100", user.ID)
		assert.Equal(t, "test@example.com", user.Email)

		user2, err := q.FindOrFail(ctx, "usr-100")
		require.NoError(t, err)
		require.NotNil(t, user2)
		assert.Equal(t, "usr-100", user2.ID)
	})

	t.Run("Find and FindOrFail when record not found", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"id", "email"}), nil // empty rows
		})

		q := database.NewQuery[models.User](db)
		user, err := q.Find(ctx, "usr-404")
		require.ErrorIs(t, err, sql.ErrNoRows)
		assert.Nil(t, user)

		user2, err := q.FindOrFail(ctx, "usr-404")
		require.ErrorIs(t, err, sql.ErrNoRows)
		assert.Nil(t, user2)
	})

	t.Run("FindMany returns records when non-empty", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"usr-1", "u1@example.com", "User 1", "user", "active"},
				[]driver.Value{"usr-2", "u2@example.com", "User 2", "user", "active"},
			), nil
		})

		q := database.NewQuery[models.User](db)
		users, err := q.FindMany(ctx, []string{"usr-1", "usr-2"})
		require.NoError(t, err)
		assert.Len(t, users, 2)
	})

	t.Run("First and FirstWhere variations", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"usr-first", "first@example.com", "First User", "admin", "active"},
			), nil
		})

		q := database.NewQuery[models.User](db)
		u1, err := q.First(ctx)
		require.NoError(t, err)
		assert.Equal(t, "usr-first", u1.ID)

		// 1 arg
		u2, err := q.FirstWhere(ctx, "email", "first@example.com")
		require.NoError(t, err)
		assert.Equal(t, "usr-first", u2.ID)

		// 2 args
		u3, err := q.FirstWhere(ctx, "role", "=", "admin")
		require.NoError(t, err)
		assert.Equal(t, "usr-first", u3.ID)

		// 0 args
		u4, err := q.FirstWhere(ctx, "banned = false")
		require.NoError(t, err)
		assert.Equal(t, "usr-first", u4.ID)
	})

	t.Run("Count returns matching rows count", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"count"}, []driver.Value{int64(42)}), nil
		})

		q := database.NewQuery[models.User](db)
		count, err := q.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 42, count)
	})

	t.Run("Exists returns true or false based on query", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"exists"}, []driver.Value{true}), nil
		})

		q := database.NewQuery[models.User](db)
		exists, err := q.Exists(ctx)
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("Paginate handles pagination and empty count shortcut", func(t *testing.T) {
		// 当 Count 为 0 时直接返回空切片
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"count"}, []driver.Value{int64(0)}), nil
		})

		q := database.NewQuery[models.User](db)
		items, total, err := q.Paginate(ctx, 0, 0)
		require.NoError(t, err)
		assert.Equal(t, 0, total)
		assert.Empty(t, items)

		// 当 Count > 0 时执行分页数据查询
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(strings.ToUpper(query), "COUNT") {
				return makeMockRows([]string{"count"}, []driver.Value{int64(5)}), nil
			}
			return makeMockRows(
				[]string{"id", "email"},
				[]driver.Value{"usr-1", "u1@example.com"},
			), nil
		})

		items, total, err = q.Paginate(ctx, 1, 10)
		require.NoError(t, err)
		assert.Equal(t, 5, total)
		assert.Len(t, items, 1)

		// 超大 pageSize 自动截断至 maxPageSize (100)
		items, total, err = q.Paginate(ctx, 1, 500)
		require.NoError(t, err)
		assert.Equal(t, 5, total)
		assert.Len(t, items, 1)
	})
}

// TestActiveRecord_Execution_AggregatesAndScalars 测试聚合函数与标量查询在实际 DB 下的执行。
func TestActiveRecord_Execution_AggregatesAndScalars(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMockDB(t)

	t.Run("Aggregate, Sum, Avg, Min, Max", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"val"}, []driver.Value{float64(88.5)}), nil
		})

		q := database.NewQuery[models.User](db)

		var custom float64
		err := q.Aggregate(ctx, "AVG(score)", &custom)
		require.NoError(t, err)
		assert.InDelta(t, 88.5, custom, 0.001)

		sum, err := q.Sum(ctx, "amount")
		require.NoError(t, err)
		assert.InDelta(t, 88.5, sum, 0.001)

		avg, err := q.Avg(ctx, "amount")
		require.NoError(t, err)
		assert.InDelta(t, 88.5, avg, 0.001)

		min, err := q.Min(ctx, "amount")
		require.NoError(t, err)
		assert.InDelta(t, 88.5, min, 0.001)

		max, err := q.Max(ctx, "amount")
		require.NoError(t, err)
		assert.InDelta(t, 88.5, max, 0.001)
	})

	t.Run("Value, Pluck, PluckStrings", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"email"}, []driver.Value{"alice@example.com"}, []driver.Value{"bob@example.com"}), nil
		})

		q := database.NewQuery[models.User](db)

		val, err := q.Value(ctx, "email")
		require.NoError(t, err)
		assert.Equal(t, "alice@example.com", val)

		emails, err := database.Pluck[string](ctx, q, "email")
		require.NoError(t, err)
		assert.Len(t, emails, 2)

		strList, err := q.PluckStrings(ctx, "email")
		require.NoError(t, err)
		assert.Len(t, strList, 2)
	})
}

// TestActiveRecord_Execution_EloquentAdvancedOperations 验证复杂条件构造、Sole、事务与高级操作。
func TestActiveRecord_Execution_EloquentAdvancedOperations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMockDB(t)

	t.Run("Sole - exactly one record vs zero vs multiple", func(t *testing.T) {
		q := database.NewQuery[models.User](db)

		// 1. 恰好 1 条记录
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"usr-sole-1", "sole@example.com", "Sole User", "user", "active"},
			), nil
		})
		user, err := q.Sole(ctx)
		require.NoError(t, err)
		require.NotNil(t, user)
		assert.Equal(t, "usr-sole-1", user.ID)

		// 2. 0 条记录 -> sql.ErrNoRows
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"id", "email"}), nil
		})
		userNil, err := q.Sole(ctx)
		require.ErrorIs(t, err, sql.ErrNoRows)
		assert.Nil(t, userNil)

		// 3. 2 条及以上记录 -> ErrMultipleRecordsFound
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"usr-1", "dupe@example.com", "User 1", "user", "active"},
				[]driver.Value{"usr-2", "dupe@example.com", "User 2", "user", "active"},
			), nil
		})
		userDupe, err := q.Sole(ctx)
		require.ErrorIs(t, err, database.ErrMultipleRecordsFound)
		assert.Nil(t, userDupe)

		// 4. 底层 DB 报错透传
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("db disconnect")
		})
		_, err = q.Sole(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db disconnect")
	})

	t.Run("FirstOr and FindOr", func(t *testing.T) {
		q := database.NewQuery[models.User](db)

		// FirstOr 命中记录
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"usr-found", "found@example.com", "Found", "user", "active"},
			), nil
		})
		u1, err := q.FirstOr(ctx, func() (*models.User, error) {
			return &models.User{Base: database.Base{ID: "usr-fallback"}}, nil
		})
		require.NoError(t, err)
		assert.Equal(t, "usr-found", u1.ID)

		// FirstOr 未命中执行 fallback
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"id", "email"}), nil
		})
		u2, err := q.FirstOr(ctx, func() (*models.User, error) {
			return &models.User{Base: database.Base{ID: "usr-fallback"}}, nil
		})
		require.NoError(t, err)
		assert.Equal(t, "usr-fallback", u2.ID)

		// FirstOr 未命中且无 fallback
		u3, err := q.FirstOr(ctx, nil)
		require.ErrorIs(t, err, sql.ErrNoRows)
		assert.Nil(t, u3)

		// FindOr 命中记录
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"usr-found-id", "id@example.com", "ID", "user", "active"},
			), nil
		})
		u4, err := q.FindOr(ctx, "usr-found-id", nil)
		require.NoError(t, err)
		assert.Equal(t, "usr-found-id", u4.ID)

		// FindOr 未命中执行 fallback
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"id", "email"}), nil
		})
		u5, err := q.FindOr(ctx, "usr-404", func() (*models.User, error) {
			return &models.User{Base: database.Base{ID: "usr-from-find-fallback"}}, nil
		})
		require.NoError(t, err)
		assert.Equal(t, "usr-from-find-fallback", u5.ID)

		// FindOr 未命中且无 fallback
		u6, err := q.FindOr(ctx, "usr-404", nil)
		require.ErrorIs(t, err, sql.ErrNoRows)
		assert.Nil(t, u6)
	})

	t.Run("ChunkByID cursor iteration and early stop", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		batchCount := 0

		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			batchCount++
			if batchCount == 1 {
				// 第一批 2 条记录
				return makeMockRows(
					[]string{"id", "email", "name", "role", "status"},
					[]driver.Value{"usr-01", "u1@test.com", "U1", "user", "active"},
					[]driver.Value{"usr-02", "u2@test.com", "U2", "user", "active"},
				), nil
			}
			// 第二批 1 条记录（< chunkSize，停止迭代）
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"usr-03", "u3@test.com", "U3", "user", "active"},
			), nil
		})

		var collected []string
		err := q.ChunkByID(ctx, 2, func(items []models.User) error {
			for _, item := range items {
				collected = append(collected, item.ID)
			}
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"usr-01", "usr-02", "usr-03"}, collected)

		// 异常中断测试
		batchCount = 0
		err = q.ChunkByID(ctx, 2, func(items []models.User) error {
			return errors.New("abort chunk by id")
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "abort chunk by id")

		// 默认 chunkSize <= 0
		batchCount = 0
		err = q.ChunkByID(ctx, 0, func(items []models.User) error {
			return nil
		})
		require.NoError(t, err)
	})

	t.Run("Restore and RestoreAll", func(t *testing.T) {
		q := database.NewQuery[models.User](db)

		// 1. models.Restore 成功
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return driver.RowsAffected(1), nil
		})
		past := time.Now().UTC().Add(-time.Hour)
		u := &models.User{Base: database.Base{ID: "usr-to-restore"}, DeletedAt: &past}
		err := database.Restore(ctx, db, u)
		require.NoError(t, err)
		assert.Nil(t, u.GetDeletedAt())

		// 2. 空 ID 报错
		err = database.Restore(ctx, db, &models.User{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot restore model with empty id")

		// 3. 0 行受影响 -> sql.ErrNoRows
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return driver.RowsAffected(0), nil
		})
		err = database.Restore(ctx, db, &models.User{Base: database.Base{ID: "usr-already-restored"}})
		require.ErrorIs(t, err, sql.ErrNoRows)

		// 4. Query[T].Restore 仓储算子
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return driver.RowsAffected(1), nil
		})
		err = q.Restore(ctx, &models.User{Base: database.Base{ID: "usr-restored-via-q"}})
		require.NoError(t, err)

		// 5. Query[T].Restore 非 SoftDeletable 实体
		type NonSoftItem struct {
			bun.BaseModel `bun:"table:non_soft"`
			ID            string `bun:"id,pk"`
		}
		qNonSoft := database.NewQuery[NonSoftItem](db)
		err = qNonSoft.Restore(ctx, &NonSoftItem{ID: "item-1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must implement SoftDeletable to restore")

		// 6. RestoreAll 成功
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			assert.Contains(t, query, `deleted_at = NULL`)
			return driver.RowsAffected(3), nil
		})
		res, err := q.RestoreAll(ctx)
		require.NoError(t, err)
		aff, _ := res.RowsAffected()
		assert.Equal(t, int64(3), aff)

		// 7. RestoreAll 非 SoftDeletable 报错
		_, err = qNonSoft.RestoreAll(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model does not support soft deletes")
	})

	t.Run("UpdateMany with Bulk execution", func(t *testing.T) {
		q := database.NewQuery[models.User](db)

		// 空切片安全短路
		err := q.UpdateMany(ctx, nil)
		require.NoError(t, err)

		// 批量更新
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return driver.RowsAffected(2), nil
		})
		u1 := &models.User{Base: database.Base{ID: "u1"}, Name: "N1"}
		u2 := &models.User{Base: database.Base{ID: "u2"}, Name: "N2"}
		err = q.UpdateMany(ctx, []*models.User{u1, u2}, "name")
		require.NoError(t, err)

		// Top-level UpdateMany
		err = database.UpdateMany(ctx, db, []*models.User{u1, u2})
		require.NoError(t, err)
	})

	t.Run("UpdateMatching and DeleteMatching", func(t *testing.T) {
		q := database.NewQuery[models.User](db)

		// UpdateMatching 空参数防御
		_, err := q.UpdateMatching(ctx, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "setValues cannot be empty")

		// UpdateMatching 成功更新
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			assert.Contains(t, query, `UPDATE "user"`)
			assert.Contains(t, query, `"status" = 'inactive'`)
			return driver.RowsAffected(5), nil
		})
		res, err := q.Where("status = ?", "pending").UpdateMatching(ctx, map[string]any{
			"status": "inactive",
		})
		require.NoError(t, err)
		aff, _ := res.RowsAffected()
		assert.Equal(t, int64(5), aff)

		// DeleteMatching 软删除模式
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			assert.Contains(t, query, `UPDATE "user"`)
			assert.Contains(t, query, `deleted_at =`)
			return driver.RowsAffected(2), nil
		})
		resDel, err := q.Where("status = ?", "banned").DeleteMatching(ctx)
		require.NoError(t, err)
		affDel, _ := resDel.RowsAffected()
		assert.Equal(t, int64(2), affDel)

		// DeleteMatching 强制物理删除模式 (WithTrashed)
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			assert.Contains(t, query, `DELETE FROM "user"`)
			return driver.RowsAffected(2), nil
		})
		resForceDel, err := q.WithTrashed().Where("status = ?", "expired").DeleteMatching(ctx)
		require.NoError(t, err)
		affForceDel, _ := resForceDel.RowsAffected()
		assert.Equal(t, int64(2), affForceDel)
	})

	t.Run("Transaction orchestration", func(t *testing.T) {
		q := database.NewQuery[models.User](db)

		// 1. Transaction 成功提交
		executed := false
		err := q.Transaction(ctx, func(txCtx context.Context) error {
			executed = true
			assert.NotNil(t, database.DBFromContext(txCtx))
			return nil
		})
		require.NoError(t, err)
		assert.True(t, executed)

		// 2. Transaction 异常回滚
		err = q.Transaction(ctx, func(txCtx context.Context) error {
			return errors.New("rollback test")
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rollback test")

		// 3. 非 *bun.DB 实例直接执行
		nonBunQ := &database.Query[models.User]{}
		executedFallback := false
		err = nonBunQ.Transaction(ctx, func(txCtx context.Context) error {
			executedFallback = true
			return nil
		})
		require.NoError(t, err)
		assert.True(t, executedFallback)
	})

	t.Run("Additional edge error branches", func(t *testing.T) {
		q := database.NewQuery[models.User](db)

		// 1. ChunkByID 查询异常
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("chunk query error")
		})
		err := q.ChunkByID(ctx, 2, func(items []models.User) error { return nil })
		require.Error(t, err)
		assert.Contains(t, err.Error(), "chunk query error")

		// 2. Restore 执行异常
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return nil, errors.New("restore exec error")
		})
		err = database.Restore(ctx, db, &models.User{Base: database.Base{ID: "usr-err"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "restore exec error")

		// 3. UpdateMatching 执行异常
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return nil, errors.New("update matching exec error")
		})
		_, err = q.Where("status = ?", "pending").UpdateMatching(ctx, map[string]any{"status": "active"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "update matching exec error")

		// 4. DeleteMatching 执行异常
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return nil, errors.New("delete matching exec error")
		})
		_, err = q.Where("status = ?", "banned").DeleteMatching(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "delete matching exec error")

		// 5. runAfterCreate 钩子失败分支
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return driver.RowsAffected(1), nil
		})
		err = database.Insert(ctx, db, &mockAfterFailItem{Base: database.Base{ID: "after-fail-item"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "after create hook error")

		// 6. UpdateMany 空切片短路
		resEmpty, err := q.UpdateMany(ctx, nil), database.UpdateMany[models.User](ctx, db, nil)
		require.NoError(t, resEmpty)
		require.NoError(t, err)
	})
}

// TestQuery_SanitizationEdgeCases 测试操作符、SQL 标识符安全清洗与大批量持久化自动分块。
func TestQuery_SanitizationEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMockDB(t)

	t.Run("operator sanitization fallback to >=", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		q.Has("accounts", "user_id", "INVALID_OPERATOR", 5)
		queryStr := q.BuildSelect().String()
		assert.Contains(t, queryStr, "(SELECT count(*) FROM accounts WHERE accounts.user_id = \"u\".id) >= 5")
	})

	t.Run("operator sanitization preserves valid operators", func(t *testing.T) {
		validOps := []string{"=", ">", ">=", "<", "<=", "!=", "<>"}
		for _, op := range validOps {
			q := database.NewQuery[models.User](db)
			q.Has("accounts", "user_id", op, 1)
			assert.Contains(t, q.BuildSelect().String(), op+" 1")
		}
	})

	t.Run("identifier sanitization strips dangerous characters", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		q.WithCount("accounts; DROP TABLE users;--", "user_id; OR 1=1--", "acc_count")
		queryStr := q.BuildSelect().String()
		// ; 和 -- 等非法 SQL 字符必须被完全剔除
		assert.NotContains(t, queryStr, ";")
		assert.NotContains(t, queryStr, "--")
		assert.Contains(t, queryStr, "accountsDROPTABLEusers")
		assert.Contains(t, queryStr, "user_idOR11")
	})

	t.Run("WithAggregate aggregate functions sanitize column", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		q.WithSum("orders;--", "user_id", "amount;--")
		q.WithAvg("orders;--", "user_id", "price;--")
		q.WithMin("orders;--", "user_id", "created_at;--")
		q.WithMax("orders;--", "user_id", "updated_at;--")
		queryStr := q.BuildSelect().String()
		assert.NotContains(t, queryStr, ";")
		assert.NotContains(t, queryStr, "--")
	})

	t.Run("InsertMany auto-chunking for > 500 items", func(t *testing.T) {
		executedQueries := 0
		db.SetOnAnyQuery(func(query string) {
			executedQueries++
		})
		defer db.SetOnAnyQuery(nil)

		items := make([]*models.User, 505)
		for i := range items {
			items[i] = &models.User{
				Base:  database.Base{ID: "usr_" + strings.Repeat("a", 16)},
				Email: "user" + string(rune('a'+i%26)) + "@example.com",
			}
		}

		err := database.InsertMany(ctx, db, items)
		require.NoError(t, err)
		// 505 条数据按 500 分块应产生 2 次执行
		assert.Equal(t, 2, executedQueries)
	})

	t.Run("Query ChunkSize chainable override", func(t *testing.T) {
		executedQueries := 0
		db.SetOnAnyQuery(func(query string) {
			executedQueries++
		})
		defer db.SetOnAnyQuery(nil)

		items := make([]*models.User, 250)
		for i := range items {
			items[i] = &models.User{
				Base:  database.Base{ID: "usr_" + strings.Repeat("b", 16)},
				Email: "custom" + string(rune('a'+i%26)) + "@example.com",
			}
		}

		q := database.NewQuery[models.User](db).ChunkSize(100)
		err := q.CreateMany(ctx, items)
		require.NoError(t, err)
		// 250 条数据按自定义 ChunkSize(100) 分块应产生 3 次执行 (100 + 100 + 50)
		assert.Equal(t, 3, executedQueries)
	})

	t.Run("WithCountWhere, WithExists, WithExistsWhere SQL generation", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			WithCountWhere("accounts", "user_id", func(sq *bun.SelectQuery) *bun.SelectQuery {
				return sq.Where("provider = ?", "google")
			}, "google_accounts_count").
			WithExists("accounts", "user_id").
			WithExistsWhere("verifications", "user_id", func(sq *bun.SelectQuery) *bun.SelectQuery {
				return sq.Where("status = ?", "pending")
			}, "has_pending_verification").
			WithSumWhere("orders", "user_id", "amount", func(sq *bun.SelectQuery) *bun.SelectQuery {
				return sq.Where("status = ?", "completed")
			}, "completed_orders_sum")

		sql := q.ToSQL()
		assert.Contains(t, sql, `SELECT "u".*`)
		assert.Contains(t, sql, `(SELECT count(*) FROM "accounts" WHERE (accounts.user_id = "u".id) AND (provider = 'google')) AS "google_accounts_count"`)
		assert.Contains(t, sql, `(EXISTS (SELECT 1 FROM accounts WHERE accounts.user_id = "u".id)) AS "accounts_exists"`)
		assert.Contains(t, sql, `(EXISTS (SELECT 1 FROM "verifications" WHERE (verifications.user_id = "u".id) AND (status = 'pending'))) AS "has_pending_verification"`)
		assert.Contains(t, sql, `(SELECT COALESCE(SUM(amount), 0) FROM "orders" WHERE (orders.user_id = "u".id) AND (status = 'completed')) AS "completed_orders_sum"`)
	})

	t.Run("Base columns preservation when explicit Select is used vs omitted", func(t *testing.T) {
		// 未指定 Select 时自动补充 ?TableAlias.*
		q1 := database.NewQuery[models.User](db).WithCount("accounts", "user_id")
		assert.Contains(t, q1.ToSQL(), `SELECT "u".*`)

		// 显式指定 Select 时不注入 ?TableAlias.*
		q2 := database.NewQuery[models.User](db).Select("id", "email").WithCount("accounts", "user_id")
		assert.NotContains(t, q2.ToSQL(), `"u".*`)
		assert.Contains(t, q2.ToSQL(), `SELECT "u"."id", "u"."email", (SELECT count(*)`)
	})

	t.Run("CountRelations executes grouped count query", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			assert.Contains(t, query, `SELECT "user_id" AS key, count(*) AS count FROM "accounts" WHERE ("user_id" IN ('u1', 'u2', 'u3')) GROUP BY "user_id"`)
			return makeMockRows(
				[]string{"key", "count"},
				[]driver.Value{"u1", int64(3)},
				[]driver.Value{"u2", int64(1)},
			), nil
		})

		q := database.NewQuery[models.User](db)
		counts, err := q.CountRelations(ctx, "accounts", "user_id", []string{" u1 ", "u1", "u2", "  ", "u3"})
		require.NoError(t, err)
		assert.Equal(t, int64(3), counts["u1"])
		assert.Equal(t, int64(1), counts["u2"])
		assert.Equal(t, int64(0), counts["u3"]) // u3 没有记录，自动填充 0
	})

	t.Run("LoadCount populates struct fields", func(t *testing.T) {
		type TestModel struct {
			bun.BaseModel `bun:"table:test_users"`
			database.Base
			AccountsCount int64 `bun:"accounts_count,scanonly"`
		}

		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"key", "count"},
				[]driver.Value{"usr-1", int64(5)},
				[]driver.Value{"usr-2", int64(2)},
			), nil
		})

		u1 := &TestModel{Base: database.Base{ID: "usr-1"}}
		u2 := &TestModel{Base: database.Base{ID: "usr-2"}}
		u3 := &TestModel{Base: database.Base{ID: "usr-3"}}

		users := []*TestModel{u1, u2, u3}

		err := database.LoadCount(ctx, db, users, "accounts", "user_id")
		require.NoError(t, err)
		assert.Equal(t, int64(5), u1.AccountsCount)
		assert.Equal(t, int64(2), u2.AccountsCount)
		assert.Equal(t, int64(0), u3.AccountsCount)
	})
}
