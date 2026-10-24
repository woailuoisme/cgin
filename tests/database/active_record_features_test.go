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

	"bgin/internal/database"
	"bgin/internal/database/models"
)

// TestActiveRecord_Execution_EloquentV13_Features 深度测试 Eloquent v13 风格的全新执行层方法。
func TestActiveRecord_Execution_EloquentV13_Features(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMockDB(t)

	t.Run("SoleValue variations and error boundaries", func(t *testing.T) {
		db.SetMockQuery(nil)
		db.SetMockExec(nil)
		db.SetOnAnyQuery(nil)
		defer func() {
			db.SetMockQuery(nil)
			db.SetMockExec(nil)
			db.SetOnAnyQuery(nil)
		}()
		q := database.NewQuery[models.User](db)

		// 1. 成功按 bun 标签匹配字段值
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"usr-sole-1", "sole@example.com", "Sole User", "admin", "active"},
			), nil
		})
		val, err := q.SoleValue(ctx, "email")
		require.NoError(t, err)
		assert.Equal(t, "sole@example.com", val)

		// 2. 成功按结构体字段名大小写不敏感匹配
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"usr-sole-1", "sole@example.com", "Sole User", "admin", "active"},
			), nil
		})
		valName, err := q.SoleValue(ctx, "Name")
		require.NoError(t, err)
		assert.Equal(t, "Sole User", valName)

		// 3. 字段不存在报错
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"usr-sole-1", "sole@example.com", "Sole User", "admin", "active"},
			), nil
		})
		_, err = q.SoleValue(ctx, "non_existent_field")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `column "non_existent_field" not found`)

		// 4. 多条记录冲突报错 (ErrMultipleRecordsFound)
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"usr-1", "u1@example.com", "User 1", "admin", "active"},
				[]driver.Value{"usr-2", "u2@example.com", "User 2", "admin", "active"},
			), nil
		})
		_, err = q.SoleValue(ctx, "email")
		require.ErrorIs(t, err, database.ErrMultipleRecordsFound)

		// 5. 无记录报错 (sql.ErrNoRows)
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows([]string{"id", "email"}), nil
		})
		_, err = q.SoleValue(ctx, "email")
		require.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("CountBy single query analytics mapping", func(t *testing.T) {
		db.SetMockQuery(nil)
		db.SetMockExec(nil)
		defer db.SetMockQuery(nil)
		q := database.NewQuery[models.User](db)

		// 1. 成功将分组聚合统计转换为 Go Map
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			assert.Contains(t, query, `COUNT(*) AS count`)
			assert.Contains(t, query, `GROUP BY "role"`)
			return makeMockRows(
				[]string{"key", "count"},
				[]driver.Value{"admin", int64(3)},
				[]driver.Value{"user", int64(15)},
			), nil
		})
		counts, err := q.CountBy(ctx, "role")
		require.NoError(t, err)
		assert.Equal(t, map[string]int64{"admin": 3, "user": 15}, counts)

		// 2. 数据库执行失败传播
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("db count error")
		})
		_, err = q.CountBy(ctx, "role")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db count error")
	})

	t.Run("SumBy single query analytics mapping", func(t *testing.T) {
		db.SetMockQuery(nil)
		db.SetMockExec(nil)
		defer db.SetMockQuery(nil)
		q := database.NewQuery[models.User](db)

		// 1. 成功将单列分组求和转换为 Go Map
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			assert.Contains(t, query, `COALESCE(SUM("balance"), 0) AS total`)
			assert.Contains(t, query, `GROUP BY "status"`)
			return makeMockRows(
				[]string{"key", "total"},
				[]driver.Value{"active", 1520.50},
				[]driver.Value{"pending", 80.00},
			), nil
		})
		sums, err := q.SumBy(ctx, "status", "balance")
		require.NoError(t, err)
		assert.Equal(t, map[string]float64{"active": 1520.50, "pending": 80.00}, sums)

		// 2. 数据库执行失败传播
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("db sum error")
		})
		_, err = q.SumBy(ctx, "status", "balance")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db sum error")
	})

	t.Run("PluckMap two columns key value extraction", func(t *testing.T) {
		db.SetMockQuery(nil)
		db.SetMockExec(nil)
		defer db.SetMockQuery(nil)
		q := database.NewQuery[models.User](db)

		// 1. 成功将两列映射为字典
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			assert.Contains(t, query, `"id"::text AS key, "email" AS value`)
			return makeMockRows(
				[]string{"key", "value"},
				[]driver.Value{"usr-1", "u1@example.com"},
				[]driver.Value{"usr-2", "u2@example.com"},
			), nil
		})
		mapping, err := q.PluckMap(ctx, "id", "email")
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"usr-1": "u1@example.com", "usr-2": "u2@example.com"}, mapping)

		// 2. 数据库执行失败传播
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("db pluck map error")
		})
		_, err = q.PluckMap(ctx, "id", "email")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db pluck map error")
	})

	t.Run("ChunkWhile batch iteration and early termination", func(t *testing.T) {
		db.SetMockQuery(nil)
		db.SetMockExec(nil)
		defer db.SetMockQuery(nil)
		q := database.NewQuery[models.User](db)

		// 1. 完整分页直至末页 (少于 chunkSize)
		pageCalls := 0
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			pageCalls++
			if pageCalls == 1 {
				return makeMockRows(
					[]string{"id", "email", "name", "role", "status"},
					[]driver.Value{"u-1", "u1@example.com", "User 1", "user", "active"},
					[]driver.Value{"u-2", "u2@example.com", "User 2", "user", "active"},
				), nil
			}
			return makeMockRows([]string{"id", "email"}), nil
		})
		var visited []string
		err := q.ChunkWhile(ctx, 2, func(users []models.User) bool {
			for _, u := range users {
				visited = append(visited, u.ID)
			}
			return true
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"u-1", "u-2"}, visited)

		// 2. 回调返回 false 主动提前中止且不报错
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"u-1", "u1@example.com", "User 1", "user", "active"},
				[]driver.Value{"u-2", "u2@example.com", "User 2", "user", "active"},
			), nil
		})
		err = q.ChunkWhile(ctx, 2, func(users []models.User) bool {
			return false
		})
		require.NoError(t, err)

		// 3. chunkSize <= 0 默认自适应回退到 100
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			assert.Contains(t, query, "LIMIT 100")
			return makeMockRows([]string{"id", "email"}), nil
		})
		err = q.ChunkWhile(ctx, 0, func(users []models.User) bool {
			return true
		})
		require.NoError(t, err)

		// 4. 查询失败传播
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("chunk query fail")
		})
		err = q.ChunkWhile(ctx, 10, func(users []models.User) bool { return true })
		require.Error(t, err)
		assert.Contains(t, err.Error(), "chunk query fail")
	})

	t.Run("ChunkByIDWhile keyset iteration and early termination", func(t *testing.T) {
		db.SetMockQuery(nil)
		db.SetMockExec(nil)
		defer db.SetMockQuery(nil)
		q := database.NewQuery[models.User](db)

		// 1. 基于游标 ID 的连续推进直至末页
		callCount := 0
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			callCount++
			if callCount == 1 {
				return makeMockRows(
					[]string{"id", "email", "name", "role", "status"},
					[]driver.Value{"u-1", "u1@example.com", "User 1", "user", "active"},
					[]driver.Value{"u-2", "u2@example.com", "User 2", "user", "active"},
				), nil
			}
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"u-3", "u3@example.com", "User 3", "user", "active"},
			), nil // 仅 1 条，少于 chunkSize 自动停止
		})
		var keysetIDs []string
		err := q.ChunkByIDWhile(ctx, 2, func(users []models.User) bool {
			for _, u := range users {
				keysetIDs = append(keysetIDs, u.ID)
			}
			return true
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"u-1", "u-2", "u-3"}, keysetIDs)

		// 2. 回调返回 false 主动提前中止
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"u-1", "u1@example.com", "User 1", "user", "active"},
				[]driver.Value{"u-2", "u2@example.com", "User 2", "user", "active"},
			), nil
		})
		err = q.ChunkByIDWhile(ctx, 2, func(users []models.User) bool {
			return false
		})
		require.NoError(t, err)

		// 3. chunkSize <= 0 默认自适应回退到 100
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			assert.Contains(t, query, "LIMIT 100")
			return makeMockRows([]string{"id", "email"}), nil
		})
		err = q.ChunkByIDWhile(ctx, -5, func(users []models.User) bool {
			return true
		})
		require.NoError(t, err)

		// 4. 模型不实现 Model 接口时安全跳出
		qNoID := database.NewQuery[mockItemNoID](db)
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"name"},
				[]driver.Value{"Item 1"},
				[]driver.Value{"Item 2"},
			), nil
		})
		noIDCalls := 0
		err = qNoID.ChunkByIDWhile(ctx, 2, func(items []mockItemNoID) bool {
			noIDCalls++
			return true
		})
		require.NoError(t, err)
		assert.Equal(t, 1, noIDCalls)

		// 5. 查询错误传播
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("chunk by id fail")
		})
		err = q.ChunkByIDWhile(ctx, 2, func(users []models.User) bool { return true })
		require.Error(t, err)
		assert.Contains(t, err.Error(), "chunk by id fail")
	})

	t.Run("LazyByID Go 1.23 iterator keyset streaming", func(t *testing.T) {
		db.SetMockQuery(nil)
		db.SetMockExec(nil)
		defer db.SetMockQuery(nil)
		q := database.NewQuery[models.User](db)

		// 1. 完整迭代 3 条记录跨 2 个 Chunk
		callCount := 0
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			callCount++
			if callCount == 1 {
				return makeMockRows(
					[]string{"id", "email", "name", "role", "status"},
					[]driver.Value{"u-1", "u1@example.com", "User 1", "user", "active"},
					[]driver.Value{"u-2", "u2@example.com", "User 2", "user", "active"},
				), nil
			}
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"u-3", "u3@example.com", "User 3", "user", "active"},
			), nil
		})
		var lazyIDs []string
		for user, err := range q.LazyByID(ctx, 2) {
			require.NoError(t, err)
			lazyIDs = append(lazyIDs, user.ID)
		}
		assert.Equal(t, []string{"u-1", "u-2", "u-3"}, lazyIDs)

		// 2. 提前 break 立即停止流式遍历
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return makeMockRows(
				[]string{"id", "email", "name", "role", "status"},
				[]driver.Value{"u-1", "u1@example.com", "User 1", "user", "active"},
				[]driver.Value{"u-2", "u2@example.com", "User 2", "user", "active"},
			), nil
		})
		breakCount := 0
		for user, err := range q.LazyByID(ctx, 2) {
			require.NoError(t, err)
			breakCount++
			if user.ID == "u-1" {
				break
			}
		}
		assert.Equal(t, 1, breakCount)

		// 3. 查询异常中断
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("lazy stream query error")
		})
		for _, err := range q.LazyByID(ctx, 2) {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "lazy stream query error")
			break
		}

		// 4. chunkSize <= 0 默认自适应回退到 100
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			assert.Contains(t, query, "LIMIT 100")
			return makeMockRows([]string{"id", "email"}), nil
		})
		for _, err := range q.LazyByID(ctx, 0) {
			require.NoError(t, err)
		}
	})

	t.Run("InsertManyChunked, UpsertChunked, UpdateManyChunked batch mutations", func(t *testing.T) {
		db.SetMockQuery(nil)
		db.SetMockExec(nil)
		db.SetOnAnyQuery(nil)
		defer func() {
			db.SetMockQuery(nil)
			db.SetMockExec(nil)
			db.SetOnAnyQuery(nil)
		}()
		q := database.NewQuery[models.User](db)

		// 1. 空数据快速短路
		require.NoError(t, q.InsertManyChunked(ctx, nil, 10))
		require.NoError(t, q.UpsertChunked(ctx, nil, 10, []string{"id"}))
		require.NoError(t, q.UpdateManyChunked(ctx, nil, 10, "name"))
		require.NoError(t, database.InsertManyChunked(ctx, db, []*models.User{}, 10))
		require.NoError(t, database.UpsertChunked(ctx, db, []*models.User{}, 10, []string{"id"}))
		require.NoError(t, database.UpdateManyChunked(ctx, db, []*models.User{}, 10, "name"))

		// 2. 批量 5 条数据分块（chunkSize = 2，共 3 块: 2, 2, 1）
		users := []*models.User{
			{Base: database.Base{ID: "u-1"}, Name: "N1"},
			{Base: database.Base{ID: "u-2"}, Name: "N2"},
			{Base: database.Base{ID: "u-3"}, Name: "N3"},
			{Base: database.Base{ID: "u-4"}, Name: "N4"},
			{Base: database.Base{ID: "u-5"}, Name: "N5"},
		}

		execCalls := 0
		db.SetOnAnyQuery(func(query string) {
			execCalls++
		})
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return mockResult{rowsAffected: 2}, nil
		})

		// Query receiver methods
		execCalls = 0
		err := q.InsertManyChunked(ctx, users, 2)
		require.NoError(t, err)
		assert.Equal(t, 3, execCalls)

		execCalls = 0
		err = q.UpsertChunked(ctx, users, 2, []string{"id"})
		require.NoError(t, err)
		assert.Equal(t, 3, execCalls)

		execCalls = 0
		err = q.UpdateManyChunked(ctx, users, 2, "name")
		require.NoError(t, err)
		assert.Equal(t, 3, execCalls)

		// Top-level database functions
		execCalls = 0
		err = database.InsertManyChunked(ctx, db, users, 2)
		require.NoError(t, err)
		assert.Equal(t, 3, execCalls)

		execCalls = 0
		err = database.UpsertChunked(ctx, db, users, 2, []string{"id"})
		require.NoError(t, err)
		assert.Equal(t, 3, execCalls)

		execCalls = 0
		err = database.UpdateManyChunked(ctx, db, users, 2, "name")
		require.NoError(t, err)
		assert.Equal(t, 3, execCalls)

		// 3. chunkSize <= 0 默认自适应回退到 500
		execCalls = 0
		err = q.InsertManyChunked(ctx, users, 0)
		require.NoError(t, err)
		assert.Equal(t, 1, execCalls)

		execCalls = 0
		err = q.UpsertChunked(ctx, users, -1, []string{"id"})
		require.NoError(t, err)
		assert.Equal(t, 1, execCalls)

		execCalls = 0
		err = q.UpdateManyChunked(ctx, users, 0, "name")
		require.NoError(t, err)
		assert.Equal(t, 1, execCalls)

		// 4. 分块执行错误中断与冒泡
		db.SetOnAnyQuery(nil)
		db.SetMockExec(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			return nil, errors.New("batch chunk exec error")
		})
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("batch chunk exec error")
		})
		require.Error(t, q.InsertManyChunked(ctx, users, 2))
		require.Error(t, q.UpsertChunked(ctx, users, 2, []string{"id"}))
		require.Error(t, q.UpdateManyChunked(ctx, users, 2, "name"))
	})

	t.Run("Load lazy eager loading", func(t *testing.T) {
		db.SetMockQuery(nil)
		db.SetMockExec(nil)
		defer db.SetMockQuery(nil)

		// 1. 空集合与空关系短路保护
		require.NoError(t, database.Load(ctx, db, nil, "Accounts"))
		require.NoError(t, database.Load(ctx, db, []*mockUserWithAccounts{}, "Accounts"))
		u1 := &mockUserWithAccounts{Base: database.Base{ID: "u-1"}}
		require.NoError(t, database.Load(ctx, db, []*mockUserWithAccounts{u1}, ""))

		// 2. 正常加载关联数据
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "mock_users") {
				return makeMockRows(
					[]string{"id", "created_at", "updated_at"},
					[]driver.Value{"u-1", time.Now(), time.Now()},
				), nil
			}
			if strings.Contains(query, "mock_accounts") {
				return makeMockRows(
					[]string{"id", "created_at", "updated_at", "user_id", "provider"},
					[]driver.Value{"acc-1", time.Now(), time.Now(), "u-1", "github"},
				), nil
			}
			return makeMockRows([]string{"id"}), nil
		})
		err := database.Load(ctx, db, []*mockUserWithAccounts{u1}, "Accounts")
		require.NoError(t, err)
		assert.Len(t, u1.Accounts, 1)
		assert.Equal(t, "acc-1", u1.Accounts[0].ID)
		assert.Equal(t, "github", u1.Accounts[0].Provider)

		// 3. 加载异常传播
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("load relation error")
		})
		err = database.Load(ctx, db, []*mockUserWithAccounts{u1}, "Accounts")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "load relation error")

		// 4. Query[T].Load 仓储方法委托测试
		db.SetMockQuery(func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "mock_users") {
				return makeMockRows(
					[]string{"id", "created_at", "updated_at"},
					[]driver.Value{"u-1", time.Now(), time.Now()},
				), nil
			}
			if strings.Contains(query, "mock_accounts") {
				return makeMockRows(
					[]string{"id", "created_at", "updated_at", "user_id", "provider"},
					[]driver.Value{"acc-2", time.Now(), time.Now(), "u-1", "google"},
				), nil
			}
			return makeMockRows([]string{"id"}), nil
		})
		q := database.NewQuery[mockUserWithAccounts](db)
		u1.Accounts = nil
		require.NoError(t, q.Load(ctx, []*mockUserWithAccounts{u1}, "Accounts"))
		assert.Len(t, u1.Accounts, 1)
		assert.Equal(t, "acc-2", u1.Accounts[0].ID)
	})
}
