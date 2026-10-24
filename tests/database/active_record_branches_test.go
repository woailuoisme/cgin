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

// TestActiveRecord_Execution_ErrorBranchesAndEdgeCases 深度覆盖各类生命周期钩子失败、DB 执行报错与边界空值路径。
func TestActiveRecord_Execution_ErrorBranchesAndEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMockDB(t)

	t.Run("Base nil receiver safety", func(t *testing.T) {
		var nilBase *database.Base
		assert.Empty(t, nilBase.GetID())
		nilBase.SetID("cannot-set") // 不会发生空指针 panic
	})

	t.Run("WhereIn and WhereNotIn with nil or non-slice values", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		q = q.WhereIn("id", []string{})
		assert.Contains(t, q.ToSQL(), "1 = 0")

		q2 := database.NewQuery[models.User](db)
		q2 = q2.WhereIn("id", "not-a-slice")
		assert.Contains(t, q2.ToSQL(), "List(non-slice string)")

		q3 := database.NewQuery[models.User](db)
		q3 = q3.WhereNotIn("id", []string{})
		assert.NotContains(t, q3.ToSQL(), "NOT IN")

		q4 := database.NewQuery[models.User](db)
		q4 = q4.WhereIn("id", nil)
		assert.Contains(t, q4.ToSQL(), "List(non-slice <nil>)")
	})

	t.Run("FindOrFail and First DB errors", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("db disk failure")
		})

		q := database.NewQuery[models.User](db)
		_, err := q.FindOrFail(ctx, "usr-err")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db disk failure")

		_, err = q.First(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db disk failure")

		_, err = q.Get(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db disk failure")
	})

	t.Run("Paginate Count error and Get error", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("count failed")
		})

		q := database.NewQuery[models.User](db)
		_, _, err := q.Paginate(ctx, 1, 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "count failed")

		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(strings.ToUpper(query), "COUNT") {
				return makeMockRows([]string{"count"}, []driver.Value{int64(10)}), nil
			}
			return nil, errors.New("page data fetch failed")
		})

		_, _, err = q.Paginate(ctx, 1, 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "page data fetch failed")
	})

	t.Run("Delete hard deletes models without SoftDeletable", func(t *testing.T) {
		var executedSQL string
		db.SetOnAnyQuery(func(query string) {
			executedSQL = query
		})

		item := &mockHardDeleteItem{Base: database.Base{ID: "hard-1"}}
		err := database.Delete(ctx, db, item)
		require.NoError(t, err)
		assert.Contains(t, executedSQL, "DELETE FROM")
	})

	t.Run("Hook failure branches: BeforeUpdate, AfterUpdate, BeforeDelete, AfterDelete, AfterCreate", func(t *testing.T) {
		// BeforeUpdate 失败
		hookItem := &mockHookItem{Base: database.Base{ID: "h-1"}, FailBeforeUpdate: true}
		err := database.Update(ctx, db, hookItem)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hook: fail before update")

		// AfterUpdate 失败
		hookItem = &mockHookItem{Base: database.Base{ID: "h-1"}, FailAfterUpdate: true}
		err = database.Update(ctx, db, hookItem)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hook: fail after update")

		// BeforeDelete 失败
		hookItem = &mockHookItem{Base: database.Base{ID: "h-1"}, FailBeforeDelete: true}
		err = database.Delete(ctx, db, hookItem)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hook: fail before delete")

		// AfterDelete 失败
		hookItem = &mockHookItem{Base: database.Base{ID: "h-1"}, FailAfterDelete: true}
		err = database.Delete(ctx, db, hookItem)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hook: fail after delete")

		// AfterCreate 失败
		hookItem = &mockHookItem{FailAfterCreate: true}
		err = database.Insert(ctx, db, hookItem)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hook: fail after create")
	})

	t.Run("DB Exec errors on Insert, Update, Delete, SoftDelete", func(t *testing.T) {
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return nil, errors.New("db write deadlock")
		})
		db.SetOnAnyQuery(nil)

		user := &models.User{Base: database.Base{ID: "u-1"}, Email: "deadlock@example.com"}

		err := database.Insert(ctx, db, user)
		require.Error(t, err)

		err = database.Update(ctx, db, user)
		require.Error(t, err)

		err = database.SoftDelete(ctx, db, user)
		require.Error(t, err)

		hardItem := &mockHardDeleteItem{Base: database.Base{ID: "hd-1"}}
		err = database.Delete(ctx, db, hardItem)
		require.Error(t, err)
	})

	t.Run("Non-Model items error on Touch and Save", func(t *testing.T) {
		type PlainStruct struct {
			Name string
		}
		qPlain := database.NewQuery[PlainStruct](db)
		err := qPlain.Touch(ctx, &PlainStruct{Name: "plain"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must implement Model interface")

		err = qPlain.Save(ctx, &PlainStruct{Name: "plain"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must implement Model interface")
	})

	t.Run("Scalar scan errors: Value, Pluck, PluckStrings", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("scan failure")
		})

		q := database.NewQuery[models.User](db)
		_, err := q.Value(ctx, "email")
		require.Error(t, err)

		_, err = database.Pluck[string](ctx, q, "email")
		require.Error(t, err)

		_, err = q.PluckStrings(ctx, "email")
		require.Error(t, err)
	})

	t.Run("Cursor error on Rows query failure", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("cursor rows network err")
		})

		q := database.NewQuery[models.User](db)
		err := q.Cursor(ctx, func(u *models.User) error { return nil })
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cursor rows network err")
	})

	t.Run("NewQueryFrom with active Tx in context and bun.DB fallback", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() {
			_ = tx.Rollback()
		}()

		ctxWithTx := database.WithDB(ctx, tx)
		q := database.NewQueryFrom[models.User](ctxWithTx, db)
		assert.NotNil(t, q)
	})

	t.Run("Increment and Decrement default delta", func(t *testing.T) {
		var executedSQL []string
		db.SetOnAnyQuery(func(query string) {
			executedSQL = append(executedSQL, query)
		})
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return driver.RowsAffected(1), nil
		})

		q := database.NewQuery[models.User](db).Where("id = ?", "u-inc")
		_, err := q.Increment(ctx, "failed_login_count")
		require.NoError(t, err)

		_, err = q.Decrement(ctx, "failed_login_count")
		require.NoError(t, err)
	})

	t.Run("Query Delete non-Model and DeleteByID error", func(t *testing.T) {
		type PlainItem struct {
			bun.BaseModel `bun:"table:plain_items,alias:pi"`
			ID            string `bun:"id,pk"`
		}
		var executedSQL string
		db.SetOnAnyQuery(func(query string) {
			executedSQL = query
		})
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return driver.RowsAffected(1), nil
		})

		qPlain := database.NewQuery[PlainItem](db)
		err := qPlain.Delete(ctx, &PlainItem{ID: "p-1"})
		require.NoError(t, err)
		assert.Contains(t, executedSQL, "DELETE FROM")

		// DeleteByID with Find error
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, sql.ErrNoRows
		})
		qUser := database.NewQuery[models.User](db)
		err = qUser.DeleteByID(ctx, "not-found")
		require.Error(t, err)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("Batch operations empty slice and validation errors", func(t *testing.T) {
		err := database.InsertMany[models.User](ctx, db, nil)
		require.NoError(t, err)

		err = database.InsertMany(ctx, db, []*models.User{})
		require.NoError(t, err)

		err = database.InsertOrIgnore[models.User](ctx, db, nil)
		require.NoError(t, err)

		err = database.Upsert[models.User](ctx, db, nil, []string{"id"}, "email")
		require.NoError(t, err)

		err = database.Upsert(ctx, db, []*models.User{{Email: "test@example.com"}}, nil, "email")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires at least one conflict column")
	})

	t.Run("Cursor requires bun.DB when scanDB is nil", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() {
			_ = tx.Rollback()
		}()

		q := database.NewQuery[models.User](tx)
		err = q.Cursor(ctx, func(u *models.User) error { return nil })
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires *bun.DB instance")
	})

	t.Run("Chunk with non-positive size and closure error", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"chk-1", "c1@test.com", "C1", "user", "active"},
			), nil
		})

		q := database.NewQuery[models.User](db)
		err := q.Chunk(ctx, 0, func(items []models.User) error {
			assert.Len(t, items, 1)
			return nil
		})
		require.NoError(t, err)

		err = q.Chunk(ctx, 10, func(items []models.User) error {
			return errors.New("chunk stop")
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "chunk stop")
	})

	t.Run("FirstOrCreate and UpdateOrCreate non-ErrNoRows db error", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("connection reset by peer")
		})

		q := database.NewQuery[models.User](db)
		_, _, err := q.FirstOrCreate(ctx, &models.User{Email: "err@test.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection reset by peer")

		_, _, err = q.UpdateOrCreate(ctx, &models.User{Email: "err@test.com"}, "name")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection reset by peer")
	})

	t.Run("SoftDelete empty ID and zero rows affected", func(t *testing.T) {
		emptyUser := &models.User{}
		err := database.SoftDelete(ctx, db, emptyUser)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot soft delete model with empty id")

		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return driver.RowsAffected(0), nil
		})
		user := &models.User{Base: database.Base{ID: "usr-already-deleted"}}
		err = database.SoftDelete(ctx, db, user)
		require.Error(t, err)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("ChangeTracker edge cases and unexported fields", func(t *testing.T) {
		var ct database.ChangeTracker
		assert.Nil(t, ct.GetDirtyWith(&models.User{}))
		assert.False(t, ct.IsDirtyWith(&models.User{}))

		ct.SyncOriginalFrom(12345)
		assert.Nil(t, ct.GetDirtyWith(12345))

		type ComplexModel struct {
			database.Base
			unexported string
			Ignored    string `bun:"-"`
			NoCol      string `bun:""`
			TableTag   string `bun:"table:ignored"`
			RelTag     string `bun:"rel:has-one"`
			ValidCol   string `bun:"valid_col"`
		}
		var ct2 database.ChangeTracker
		cm := &ComplexModel{
			unexported: "secret",
			Ignored:    "skip",
			ValidCol:   "initial",
		}
		ct2.SyncOriginalFrom(cm)
		assert.False(t, ct2.IsDirtyWith(cm))

		cm.ValidCol = "modified"
		assert.True(t, ct2.IsDirtyWith(cm, "valid_col"))
		assert.ElementsMatch(t, []string{"valid_col"}, ct2.GetDirtyWith(cm))
	})
}

// TestActiveRecord_Execution_ExhaustiveBranchCoverage 针对全量边界与异常分支进行 100% 覆盖补齐。
func TestActiveRecord_Execution_ExhaustiveBranchCoverage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMockDB(t)

	t.Run("Load nil pointer and slice pointer variations", func(t *testing.T) {
		db.SetMockQuery(nil)
		db.SetMockExec(nil)
		defer db.SetMockQuery(nil)

		// nil 指针与空切片指针短路，以及非法非指针/非切片默认分支覆盖
		require.NoError(t, database.Load(ctx, db, (*mockUserWithAccounts)(nil), "Accounts"))
		require.NoError(t, database.Load(ctx, db, &[]*mockUserWithAccounts{}, "Accounts"))
		require.Error(t, database.Load(ctx, db, "invalid_model_type", "Accounts"))

		// 单个实体指针预加载
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "mock_users") {
				return makeMockRows(
					[]string{"id", "created_at", "updated_at"},
					[]driver.Value{"u-solo", time.Now(), time.Now()},
				), nil
			}
			if strings.Contains(query, "mock_accounts") {
				return makeMockRows(
					[]string{"id", "created_at", "updated_at", "user_id", "provider"},
					[]driver.Value{"acc-solo", time.Now(), time.Now(), "u-solo", "google"},
				), nil
			}
			return makeMockRows([]string{"id"}), nil
		})
		single := &mockUserWithAccounts{Base: database.Base{ID: "u-solo"}}
		require.NoError(t, database.Load(ctx, db, single, "Accounts"))
		assert.Len(t, single.Accounts, 1)
		assert.Equal(t, "google", single.Accounts[0].Provider)
	})

	t.Run("First and Get dirty trackable sync", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "title"},
				[]driver.Value{"track-1", "Original Title"},
			), nil
		})
		defer db.SetMockQuery(nil)

		q := database.NewQuery[mockTrackableItem](db)
		item, err := q.First(ctx)
		require.NoError(t, err)
		assert.False(t, item.IsDirty())

		items, err := q.Get(ctx)
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.False(t, items[0].IsDirty())
	})

	t.Run("Pluck when database returns empty rows", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"email"}), nil
		})
		defer db.SetMockQuery(nil)

		q := database.NewQuery[models.User](db)
		emails, err := database.Pluck[string](ctx, q, "email")
		require.NoError(t, err)
		assert.NotNil(t, emails)
		assert.Empty(t, emails)
	})

	t.Run("FirstOrCreate and UpdateOrCreate create failure branches", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "INSERT") {
				return nil, errors.New("simulated insert fail")
			}
			return makeMockRows([]string{"id", "email"}), nil // empty -> ErrNoRows
		})
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return nil, errors.New("simulated insert fail")
		})
		defer func() {
			db.SetMockQuery(nil)
			db.SetMockExec(nil)
		}()

		q := database.NewQuery[models.User](db).Where("email = ?", "fail@example.com")
		_, created, err := q.FirstOrCreate(ctx, &models.User{Email: "fail@example.com"})
		require.Error(t, err)
		assert.False(t, created)
		assert.Contains(t, err.Error(), "simulated insert fail")

		_, created, err = q.UpdateOrCreate(ctx, &models.User{Email: "fail@example.com"})
		require.Error(t, err)
		assert.False(t, created)
		assert.Contains(t, err.Error(), "simulated insert fail")

		// UpdateOrCreate update failure
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "UPDATE") {
				return nil, errors.New("simulated update fail")
			}
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"existing-fail", "fail@example.com", "Name", "user", "active"},
			), nil
		})
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return nil, errors.New("simulated update fail")
		})
		_, created, err = q.UpdateOrCreate(ctx, &models.User{Email: "fail@example.com", Name: "New Name"})
		require.Error(t, err)
		assert.False(t, created)
		assert.Contains(t, err.Error(), "simulated update fail")
	})

	t.Run("Save dirty tracking on new record and runAfterCreate success", func(t *testing.T) {
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return mockResult{rowsAffected: 1}, nil
		})
		defer db.SetMockExec(nil)

		newItem := &mockAfterSuccessItem{}
		err := database.Save(ctx, db, newItem)
		require.NoError(t, err)
		assert.True(t, newItem.AfterCalled)
		assert.False(t, newItem.IsDirty())

		items := []*mockAfterSuccessItem{
			{Base: database.Base{ID: "succ-1"}},
			{Base: database.Base{ID: "succ-2"}},
		}
		err = database.InsertMany(ctx, db, items)
		require.NoError(t, err)
		assert.True(t, items[0].AfterCalled)
		assert.True(t, items[1].AfterCalled)
	})

	t.Run("InsertOrIgnore before hook error", func(t *testing.T) {
		err := database.InsertOrIgnore(ctx, db, []*mockBeforeFailItem{{Base: database.Base{ID: "bf-1"}}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "before create hook error")
	})

	t.Run("Chunk, ChunkWhile, ChunkByID zero rows on first page", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"id", "email"}), nil
		})
		defer db.SetMockQuery(nil)

		q := database.NewQuery[models.User](db)
		chunkCalls := 0
		err := q.Chunk(ctx, 10, func(items []models.User) error {
			chunkCalls++
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 0, chunkCalls)

		err = q.ChunkWhile(ctx, 10, func(items []models.User) bool {
			chunkCalls++
			return true
		})
		require.NoError(t, err)
		assert.Equal(t, 0, chunkCalls)

		err = q.ChunkByID(ctx, 10, func(items []models.User) error {
			chunkCalls++
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 0, chunkCalls)
	})

	t.Run("Lazy cursor without *bun.DB instance and query failure", func(t *testing.T) {
		qNil := database.NewQuery[models.User](nil)
		for _, err := range qNil.Lazy(ctx) {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cursor streaming requires *bun.DB instance")
		}

		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("lazy query rows fail")
		})
		defer db.SetMockQuery(nil)

		q := database.NewQuery[models.User](db)
		for _, err := range q.Lazy(ctx) {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "lazy query rows fail")
			break
		}
	})

	t.Run("Transaction fallback when db is neither *bun.DB nor bun.Tx", func(t *testing.T) {
		qNil := database.NewQuery[models.User](nil)
		assert.Nil(t, qNil.BuildSelect())
		ran := false
		err := qNil.Transaction(ctx, func(txCtx context.Context) error {
			ran = true
			return nil
		})
		require.NoError(t, err)
		assert.True(t, ran)
	})

	t.Run("GetDirtyWith non-existent column in original", func(t *testing.T) {
		type mockModelWithExtra struct {
			ID    string `bun:"id"`
			Extra string `bun:"extra"`
		}
		type mockModelWithoutExtra struct {
			ID string `bun:"id"`
		}
		var ct database.ChangeTracker
		ct.SyncOriginalFrom(&mockModelWithExtra{ID: "1", Extra: "initial"})
		dirty := ct.GetDirtyWith(&mockModelWithoutExtra{ID: "1"})
		assert.Contains(t, dirty, "extra")
	})

	t.Run("SoftDelete and Restore RowsAffected error", func(t *testing.T) {
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return mockErrorResult{err: errors.New("driver rows affected error")}, nil
		})
		defer db.SetMockExec(nil)

		user := &models.User{Base: database.Base{ID: "u-err"}}
		err := database.SoftDelete(ctx, db, user)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "driver rows affected error")

		err = database.Restore(ctx, db, user)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "driver rows affected error")
	})

	t.Run("Transaction with bun.Tx receiver", func(t *testing.T) {
		err := db.RunInTx(ctx, nil, func(c context.Context, tx bun.Tx) error {
			qTx := database.NewQuery[models.User](tx)
			ran := false
			err := qTx.Transaction(c, func(txCtx context.Context) error {
				ran = true
				assert.NotNil(t, database.DBFromContext(txCtx))
				return nil
			})
			require.NoError(t, err)
			assert.True(t, ran)
			return nil
		})
		require.NoError(t, err)
	})

	t.Run("Save failure branches for new and existing models", func(t *testing.T) {
		// New item with BeforeCreate error
		newItem := &mockBeforeFailItem{}
		err := database.Save(ctx, db, newItem)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "before create hook error")

		// Existing dirty item with Update error
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "UPDATE") {
				return nil, errors.New("simulated update fail")
			}
			return makeMockRows([]string{"id"}), nil
		})
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return nil, errors.New("simulated update fail")
		})
		defer func() {
			db.SetMockQuery(nil)
			db.SetMockExec(nil)
		}()

		existingItem := &mockTrackableItem{Base: database.Base{ID: "ex-1"}, Title: "Old"}
		existingItem.SyncOriginal()
		existingItem.Title = "New"
		err = database.Save(ctx, db, existingItem)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "simulated update fail")
	})

	t.Run("runAfterCreate hook failure in InsertMany", func(t *testing.T) {
		afterFailItems := []*mockAfterFailItem{
			{Base: database.Base{ID: "af-1"}},
		}
		err := database.InsertMany(ctx, db, afterFailItems)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "after create hook error")
	})

	t.Run("InsertOrIgnore execution failure", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "INSERT") {
				return nil, errors.New("insert or ignore exec fail")
			}
			return makeMockRows([]string{"id"}), nil
		})
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return nil, errors.New("insert or ignore exec fail")
		})
		defer func() {
			db.SetMockQuery(nil)
			db.SetMockExec(nil)
		}()

		err := database.InsertOrIgnore(ctx, db, []*mockAfterSuccessItem{{Base: database.Base{ID: "ioi-1"}}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "insert or ignore exec fail")
	})

	t.Run("Chunk query execution failure", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("chunk query fail")
		})
		defer db.SetMockQuery(nil)

		q := database.NewQuery[models.User](db)
		err := q.Chunk(ctx, 10, func(items []models.User) error { return nil })
		require.Error(t, err)
		assert.Contains(t, err.Error(), "chunk query fail")
	})

	t.Run("ChunkByID with item not implementing Model", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"name"}, []driver.Value{"item1"}), nil
		})
		defer db.SetMockQuery(nil)

		qNoID := database.NewQuery[mockItemNoID](db)
		chunkCalls := 0
		err := qNoID.ChunkByID(ctx, 1, func(items []mockItemNoID) error {
			chunkCalls++
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 1, chunkCalls)
	})

	t.Run("ChunkWhile with less than chunkSize rows and true return", func(t *testing.T) {
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"id", "email"}, []driver.Value{"u1", "u1@example.com"}), nil
		})
		defer db.SetMockQuery(nil)

		q := database.NewQuery[models.User](db)
		called := 0
		err := q.ChunkWhile(ctx, 10, func(items []models.User) bool {
			called++
			return true
		})
		require.NoError(t, err)
		assert.Equal(t, 1, called)
	})

	t.Run("GetDirtyWith with non-struct input", func(t *testing.T) {
		var ct database.ChangeTracker
		ct.SyncOriginalFrom(&models.User{Email: "test@example.com"})
		dirty := ct.GetDirtyWith("not-a-struct")
		assert.Nil(t, dirty)
	})

	t.Run("Lazy rows.Err and ScanRow failure branches", func(t *testing.T) {
		// rows.Err() error
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRowsWithErr([]string{"id", "email"}, errors.New("stream rows error")), nil
		})
		q := database.NewQuery[models.User](db)
		for _, err := range q.Lazy(ctx) {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "stream rows error")
		}

		// ScanRow error
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"id"}, []driver.Value{make(chan int)}), nil
		})
		defer db.SetMockQuery(nil)

		for _, err := range q.Lazy(ctx) {
			require.Error(t, err)
			break
		}
	})

	t.Run("Batch DML and Active Record helpers propagate context transactions", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		ctxWithTx := database.WithDB(ctx, tx)

		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return driver.RowsAffected(1), nil
		})
		defer db.SetMockExec(nil)

		// 1. Increment & Decrement with context Tx
		q := database.NewQuery[models.User](db).Where("id = ?", "u-tx")
		_, err = q.Increment(ctxWithTx, "failed_login_count")
		require.NoError(t, err)

		_, err = q.Decrement(ctxWithTx, "failed_login_count")
		require.NoError(t, err)

		// 2. UpdateMatching with context Tx
		_, err = q.UpdateMatching(ctxWithTx, map[string]any{"status": "active"})
		require.NoError(t, err)

		// 3. DeleteMatching with context Tx
		_, err = q.DeleteMatching(ctxWithTx)
		require.NoError(t, err)

		// 4. RestoreAll with context Tx
		_, err = q.RestoreAll(ctxWithTx)
		require.NoError(t, err)

		// 5. Truncate with context Tx
		err = q.Truncate(ctxWithTx)
		require.NoError(t, err)

		// 6. User methods with context Tx
		user := &models.User{Base: database.Base{ID: "u-tx"}}
		err = user.MarkEmailVerified(ctxWithTx, db)
		require.NoError(t, err)

		err = user.RecordLogin(ctxWithTx, db, "password")
		require.NoError(t, err)

		err = user.Ban(ctxWithTx, db, "violating terms", nil)
		require.NoError(t, err)

		err = user.Unban(ctxWithTx, db)
		require.NoError(t, err)

		err = models.RecordLogin(ctxWithTx, db, "u-tx", "oauth")
		require.NoError(t, err)

		err = models.UpdateUserFields(ctxWithTx, db, "u-tx", map[string]any{"username": "newname"})
		require.NoError(t, err)

		// 7. Account & Verification helpers with context Tx
		err = models.UpdatePassword(ctxWithTx, db, "u-tx", "newhash")
		require.NoError(t, err)

		acc := &models.Account{Base: database.Base{ID: "acc-tx"}}
		err = acc.UpdatePassword(ctxWithTx, db, "newhash2")
		require.NoError(t, err)

		consumed, err := models.ConsumeVerification(ctxWithTx, db, "email:a@b.com", "123456")
		require.NoError(t, err)
		assert.True(t, consumed)
	})

	t.Run("Clause operator sanitization prevents SQL injection", func(t *testing.T) {
		qInvalid := database.NewQuery[models.User](db).
			WhereAny([]string{"name"}, "INVALID_OP; DROP TABLE users; --", "val")
		assert.Contains(t, qInvalid.ToSQL(), "name = 'val'")

		qAllInvalid := database.NewQuery[models.User](db).
			WhereAll([]string{"role"}, "HACK", "admin")
		assert.Contains(t, qAllInvalid.ToSQL(), "role = 'admin'")

		qColInvalid := database.NewQuery[models.User](db).
			WhereColumn("updated_at", "INJECTION", "created_at")
		assert.Contains(t, qColInvalid.ToSQL(), `"updated_at" = "created_at"`)
	})
}
