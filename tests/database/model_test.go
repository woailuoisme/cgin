// Package database_test 验证数据模型与数据库基础设施的各项契约。
package database_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/schema"

	"bgin/internal/database"
	"bgin/internal/database/models"
	"bgin/internal/shared/enums"
	"bgin/tests/testutil"
)

// TestIsBanned 校验封禁判定把已过期的封禁视为解除。
func TestIsBanned(t *testing.T) {
	now := time.Now().UTC()
	expired := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	tests := []struct {
		name string
		user models.User
		want bool
	}{
		{"未封禁", models.User{}, false},
		{"永久封禁（无到期时间）", models.User{Banned: true}, true},
		{"封禁未到期", models.User{Banned: true, BanExpires: &future}, true},
		{"封禁已到期", models.User{Banned: true, BanExpires: &expired}, false},
		{"只有到期时间但未打封禁标记", models.User{BanExpires: &future}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.user.IsBanned(now))
		})
	}
}

// TestRoleAndStatusConstants 校验角色与状态的取值与 schema 的 CHECK 约束一致。
func TestRoleAndStatusConstants(t *testing.T) {
	assert.Equal(t, enums.RoleUser, models.RoleUser)
	assert.Equal(t, enums.RoleAdmin, models.RoleAdmin)

	assert.Equal(t, enums.StatusActive, models.StatusActive)
	assert.Equal(t, enums.StatusInactive, models.StatusInactive)
	assert.Equal(t, enums.StatusBanned, models.StatusBanned)

	assert.Equal(t, "credential", models.ProviderCredential)
}

// TestActiveRecord_Interfaces 校验各实体对 Model 与 SoftDeletable 契约的静态实现。
func TestActiveRecord_Interfaces(t *testing.T) {
	var user models.User
	user.SetID("usr-123")
	assert.Equal(t, "usr-123", user.GetID())

	var model database.Model = &user
	assert.Equal(t, "usr-123", model.GetID())

	now := time.Now().UTC()
	var soft database.SoftDeletable = &user
	assert.Nil(t, soft.GetDeletedAt())
	soft.SetDeletedAt(&now)
	assert.Equal(t, &now, soft.GetDeletedAt())
	assert.Equal(t, now, user.UpdatedAt)

	var account models.Account
	account.SetID("acc-456")
	assert.Equal(t, "acc-456", account.GetID())
	var accModel database.Model = &account
	assert.Equal(t, "acc-456", accModel.GetID())

	var verification models.Verification
	verification.SetID("ver-789")
	assert.Equal(t, "ver-789", verification.GetID())
	var verModel database.Model = &verification
	assert.Equal(t, "ver-789", verModel.GetID())
}

// TestActiveRecord_UserQueryScopes 验证用户查询作用域与链式构造能力。
func TestActiveRecord_UserQueryScopes(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("ActiveUsersQuery contains deleted_at filter", func(t *testing.T) {
		q := models.ActiveUsersQuery(db)
		sql := q.String()
		assert.Contains(t, sql, "deleted_at IS NULL")
	})

	t.Run("ScopeUserFilter with status, role and search", func(t *testing.T) {
		filter := models.UserListFilter{
			Status: enums.StatusActive,
			Role:   enums.RoleAdmin,
			Search: "alice",
			Sort:   []string{"u.email ASC"},
		}
		q := models.UserQuery(db).
			Scope(models.ScopeUserFilter(filter)).
			Scope(models.ScopeUserSort(filter.Sort))
		sql := q.RawQuery().String()
		assert.Contains(t, sql, `status = 'active'`)
		assert.Contains(t, sql, `role = 'admin'`)
		assert.Contains(t, sql, `name ILIKE '%alice%'`)
		assert.Contains(t, sql, `ORDER BY "u"."email" ASC`)
	})

	t.Run("WithTrashed preserves builder", func(t *testing.T) {
		q := models.UserQuery(db).WithTrashed()
		assert.NotNil(t, q)
	})
}

// TestActiveRecord_BaseHooks 校验 Base 模型的生命周期钩子与时间戳行为。
func TestActiveRecord_BaseHooks(t *testing.T) {
	ctx := context.Background()
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("BeforeAppendModel generates UUIDv7 on insert", func(t *testing.T) {
		var base database.Base
		insertQ := db.NewInsert()
		err := base.BeforeAppendModel(ctx, insertQ)
		require.NoError(t, err)
		assert.NotEmpty(t, base.ID)
		assert.False(t, base.CreatedAt.IsZero())
		assert.False(t, base.UpdatedAt.IsZero())
		assert.Equal(t, base.CreatedAt, base.UpdatedAt)
	})

	t.Run("BeforeAppendModel preserves existing ID on insert", func(t *testing.T) {
		base := database.Base{ID: "custom-id-123"}
		insertQ := db.NewInsert()
		err := base.BeforeAppendModel(ctx, insertQ)
		require.NoError(t, err)
		assert.Equal(t, "custom-id-123", base.ID)
	})

	t.Run("BeforeAppendModel updates only UpdatedAt on update", func(t *testing.T) {
		created := time.Now().UTC().Add(-time.Hour)
		base := database.Base{
			ID:        "existing-id",
			CreatedAt: created,
			UpdatedAt: created,
		}
		updateQ := db.NewUpdate()
		err := base.BeforeAppendModel(ctx, updateQ)
		require.NoError(t, err)
		assert.Equal(t, "existing-id", base.ID)
		assert.Equal(t, created, base.CreatedAt)
		assert.True(t, base.UpdatedAt.After(created))
	})

	t.Run("UTCPtr helper converts timezone to UTC", func(t *testing.T) {
		assert.Nil(t, database.UTCPtr(nil))

		loc := time.FixedZone("CST", 8*3600)
		cstTime := time.Date(2026, 10, 1, 12, 0, 0, 0, loc)
		utcTime := database.UTCPtr(&cstTime)
		assert.NotNil(t, utcTime)
		assert.Equal(t, time.UTC, utcTime.Location())
		assert.Equal(t, 4, utcTime.Hour()) // 12:00 CST == 04:00 UTC
	})
}

// TestActiveRecord_CarbonHelpers 校验 Base 与 SoftDeletes 的 Carbon 时间转换辅助方法。
func TestActiveRecord_CarbonHelpers(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	t.Run("Base CreatedAtCarbon and UpdatedAtCarbon", func(t *testing.T) {
		var nilBase *database.Base
		assert.True(t, nilBase.CreatedAtCarbon().IsZero())
		assert.True(t, nilBase.UpdatedAtCarbon().IsZero())

		zeroBase := &database.Base{}
		assert.True(t, zeroBase.CreatedAtCarbon().IsZero())
		assert.True(t, zeroBase.UpdatedAtCarbon().IsZero())

		validBase := &database.Base{
			CreatedAt: now,
			UpdatedAt: now.Add(time.Hour),
		}
		assert.False(t, validBase.CreatedAtCarbon().IsZero())
		assert.Equal(t, 2026, validBase.CreatedAtCarbon().Year())
		assert.False(t, validBase.UpdatedAtCarbon().IsZero())
		assert.Equal(t, 13, validBase.UpdatedAtCarbon().Hour())
	})

	t.Run("SoftDeletes methods and DeletedAtCarbon", func(t *testing.T) {
		var nilSoft *database.SoftDeletes
		assert.Nil(t, nilSoft.GetDeletedAt())
		nilSoft.SetDeletedAt(&now)
		assert.True(t, nilSoft.DeletedAtCarbon().IsZero())

		zeroSoft := &database.SoftDeletes{}
		assert.Nil(t, zeroSoft.GetDeletedAt())
		assert.True(t, zeroSoft.DeletedAtCarbon().IsZero())

		zeroTime := time.Time{}
		zeroTimeSoft := &database.SoftDeletes{DeletedAt: &zeroTime}
		assert.True(t, zeroTimeSoft.DeletedAtCarbon().IsZero())

		validSoft := &database.SoftDeletes{}
		validSoft.SetDeletedAt(&now)
		assert.Equal(t, &now, validSoft.GetDeletedAt())
		assert.False(t, validSoft.DeletedAtCarbon().IsZero())
		assert.Equal(t, 12, validSoft.DeletedAtCarbon().Hour())
	})

	t.Run("DeletedAtCarbon top-level helper", func(t *testing.T) {
		assert.True(t, database.DeletedAtCarbon(nil).IsZero())

		user := &models.User{}
		assert.True(t, database.DeletedAtCarbon(user).IsZero())

		zeroTime := time.Time{}
		user.SetDeletedAt(&zeroTime)
		assert.True(t, database.DeletedAtCarbon(user).IsZero())

		user.SetDeletedAt(&now)
		c := database.DeletedAtCarbon(user)
		assert.False(t, c.IsZero())
		assert.Equal(t, 2026, c.Year())
	})
}

// TestActiveRecord_ContextDB 校验上下文数据库与事务的注入与透明感知。
func TestActiveRecord_ContextDB(t *testing.T) {
	ctx := context.Background()
	defaultDB := bun.NewDB(nil, pgdialect.New())
	txDB := bun.NewDB(nil, pgdialect.New())

	t.Run("ResolveDB returns fallback when context has no DB", func(t *testing.T) {
		resolved := database.ResolveDB(ctx, defaultDB)
		assert.Equal(t, defaultDB, resolved)
	})

	t.Run("ResolveDB returns context DB when injected via WithDB", func(t *testing.T) {
		scopedCtx := database.WithDB(ctx, txDB)
		assert.Equal(t, txDB, database.DBFromContext(scopedCtx))
		resolved := database.ResolveDB(scopedCtx, defaultDB)
		assert.Equal(t, txDB, resolved)
	})

	t.Run("NewQueryFrom resolves DB from context", func(t *testing.T) {
		scopedCtx := database.WithDB(ctx, txDB)
		q := database.NewQueryFrom[models.User](scopedCtx, defaultDB)
		assert.NotNil(t, q)

		userQ := models.UserQueryFrom(scopedCtx, defaultDB)
		assert.NotNil(t, userQ)

		accQ := models.AccountQueryFrom(scopedCtx, defaultDB)
		assert.NotNil(t, accQ)

		verQ := models.VerificationQueryFrom(scopedCtx, defaultDB)
		assert.NotNil(t, verQ)
	})
}

// TestActiveRecord_QueryOperators 校验 WhereIf、WithTrashed、OnlyTrashed 以及 BuildSelect 软删除过滤行为。
func TestActiveRecord_QueryOperators(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("WhereIf applies where clause conditionally", func(t *testing.T) {
		q1 := database.NewQuery[models.User](db).WhereIf(true, "role = ?", "admin")
		assert.Contains(t, q1.RawQuery().String(), "role = 'admin'")

		q2 := database.NewQuery[models.User](db).WhereIf(false, "role = ?", "admin")
		assert.NotContains(t, q2.RawQuery().String(), "role = 'admin'")
	})

	t.Run("BuildSelect excludes soft deleted records by default", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		sql := q.BuildSelect().String()
		assert.Contains(t, sql, "deleted_at IS NULL")
		assert.NotContains(t, sql, "deleted_at IS NOT NULL")
	})

	t.Run("BuildSelect includes soft deleted records with WithTrashed", func(t *testing.T) {
		q := database.NewQuery[models.User](db).WithTrashed()
		sql := q.BuildSelect().String()
		assert.NotContains(t, sql, "deleted_at IS NULL")
		assert.NotContains(t, sql, "deleted_at IS NOT NULL")
	})

	t.Run("BuildSelect filters only soft deleted records with OnlyTrashed", func(t *testing.T) {
		q := database.NewQuery[models.User](db).OnlyTrashed()
		sql := q.BuildSelect().String()
		assert.Contains(t, sql, "deleted_at IS NOT NULL")
	})
}

// TestActiveRecord_FluentQueryExpressions 验证流式查询表达式与条件分支（When, WhereIn, WhereNotIn, WhereNull, WhereNotNull, LockForUpdate, SharedLock）。
func TestActiveRecord_FluentQueryExpressions(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("When executes branch conditionally", func(t *testing.T) {
		q1 := database.NewQuery[models.User](db).When(true, func(q *database.Query[models.User]) *database.Query[models.User] {
			return q.Where("status = ?", "active")
		})
		assert.Contains(t, q1.RawQuery().String(), "status = 'active'")

		q2 := database.NewQuery[models.User](db).When(false, func(q *database.Query[models.User]) *database.Query[models.User] {
			return q.Where("status = ?", "active")
		})
		assert.NotContains(t, q2.RawQuery().String(), "status = 'active'")

		q3 := database.NewQuery[models.User](db).When(false, func(q *database.Query[models.User]) *database.Query[models.User] {
			return q.Where("status = ?", "active")
		}, func(q *database.Query[models.User]) *database.Query[models.User] {
			return q.Where("status = ?", "fallback")
		})
		assert.Contains(t, q3.RawQuery().String(), "status = 'fallback'")
	})

	t.Run("WhereIn with populated slice generates IN clause", func(t *testing.T) {
		q := database.NewQuery[models.User](db).WhereIn("role", []string{"admin", "user"})
		assert.Contains(t, q.RawQuery().String(), "role IN ('admin', 'user')")
	})

	t.Run("WhereIn with empty slice generates 1 = 0 safe condition", func(t *testing.T) {
		q := database.NewQuery[models.User](db).WhereIn("id", []string{})
		assert.Contains(t, q.RawQuery().String(), "1 = 0")
	})

	t.Run("WhereNotIn with populated slice generates NOT IN clause", func(t *testing.T) {
		q := database.NewQuery[models.User](db).WhereNotIn("status", []string{"banned"})
		assert.Contains(t, q.RawQuery().String(), "status NOT IN ('banned')")
	})

	t.Run("WhereNotIn with empty slice leaves query unchanged", func(t *testing.T) {
		q := database.NewQuery[models.User](db).WhereNotIn("status", []string{})
		assert.NotContains(t, q.RawQuery().String(), "NOT IN")
	})

	t.Run("WhereNull and WhereNotNull generate IS NULL / IS NOT NULL clauses", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			WhereNull("deleted_at").
			WhereNotNull("email")
		sql := q.RawQuery().String()
		assert.Contains(t, sql, "deleted_at IS NULL")
		assert.Contains(t, sql, "email IS NOT NULL")
	})

	t.Run("LockForUpdate and SharedLock set FOR UPDATE and FOR SHARE", func(t *testing.T) {
		q1 := database.NewQuery[models.User](db).LockForUpdate()
		assert.Contains(t, q1.RawQuery().String(), "FOR UPDATE")

		q2 := database.NewQuery[models.User](db).SharedLock()
		assert.Contains(t, q2.RawQuery().String(), "FOR SHARE")
	})

	t.Run("WhereOr, Order, Offset, Limit chain correctly", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			Where("status = ?", "active").
			WhereOr("role = ?", "admin").
			Order("created_at DESC").
			Offset(10).
			Limit(5)
		sql := q.RawQuery().String()
		assert.Contains(t, sql, "status = 'active'")
		assert.Contains(t, sql, "role = 'admin'")
		assert.Contains(t, sql, `ORDER BY "created_at" DESC`)
		assert.Contains(t, sql, "LIMIT 5")
		assert.Contains(t, sql, "OFFSET 10")
	})
}

// mockHookModel 用于测试各生命周期钩子拦截契约。
type mockHookModel struct {
	database.Base
	beforeCreateErr error
	beforeUpdateErr error
	beforeDeleteErr error
}

func (m *mockHookModel) BeforeCreate(_ context.Context) error {
	return m.beforeCreateErr
}

func (m *mockHookModel) BeforeUpdate(_ context.Context) error {
	return m.beforeUpdateErr
}

func (m *mockHookModel) BeforeDelete(_ context.Context) error {
	return m.beforeDeleteErr
}

// TestActiveRecord_LifecycleHooks 校验模型生命周期拦截契约（BeforeCreate, BeforeUpdate, BeforeDelete）。
func TestActiveRecord_LifecycleHooks(t *testing.T) {
	ctx := context.Background()
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("Insert triggers BeforeCreate and halts on error", func(t *testing.T) {
		errExpected := errors.New("abort insert before create")
		item := &mockHookModel{beforeCreateErr: errExpected}
		err := database.Insert(ctx, db, item)
		require.ErrorIs(t, err, errExpected)
	})

	t.Run("Update triggers BeforeUpdate and halts on error", func(t *testing.T) {
		errExpected := errors.New("abort update before update")
		item := &mockHookModel{beforeUpdateErr: errExpected}
		err := database.Update(ctx, db, item)
		require.ErrorIs(t, err, errExpected)
	})

	t.Run("Delete triggers BeforeDelete and halts on error", func(t *testing.T) {
		errExpected := errors.New("abort delete before delete")
		item := &mockHookModel{beforeDeleteErr: errExpected}
		err := database.Delete(ctx, db, item)
		require.ErrorIs(t, err, errExpected)
	})
}

// nonModelItem 声明未实现 Model 接口的测试类型。
type nonModelItem struct {
	Name string
}

// TestActiveRecord_RepositoryValidation 校验仓储层对非 Model 实体以及非法操作的安全校验。
func TestActiveRecord_RepositoryValidation(t *testing.T) {
	ctx := context.Background()
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("Query.Save rejects item not implementing Model", func(t *testing.T) {
		q := database.NewQuery[nonModelItem](db)
		err := q.Save(ctx, &nonModelItem{Name: "test"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must implement Model interface to save")
	})

	t.Run("Query.Touch rejects item not implementing Model", func(t *testing.T) {
		q := database.NewQuery[nonModelItem](db)
		err := q.Touch(ctx, &nonModelItem{Name: "test"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must implement Model interface to touch")
	})

	t.Run("SoftDelete rejects model with empty ID", func(t *testing.T) {
		user := &models.User{}
		err := database.SoftDelete(ctx, db, user)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot soft delete model with empty id")
	})
}

// TestActiveRecord_Candidate1_RelationalDepth 验证关系预加载与高级子查询过滤能力（With, WhereExists, WhereNotExists, WhereHas, WhereDoesntHave）。
func TestActiveRecord_Candidate1_RelationalDepth(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("With registers relation and custom scope callback", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			With("Accounts", func(sq *bun.SelectQuery) *bun.SelectQuery {
				return sq.Where("provider = ?", "credential")
			})
		require.NotNil(t, q)
	})

	t.Run("WhereExists and WhereNotExists generate subquery clauses", func(t *testing.T) {
		subquery := db.NewSelect().Table("accounts").Where("accounts.user_id = u.id")

		qExists := database.NewQuery[models.User](db).WhereExists(subquery)
		sqlExists := qExists.ToSQL()
		assert.Contains(t, sqlExists, `EXISTS (SELECT * FROM "accounts" WHERE (accounts.user_id = u.id))`)

		qNotExists := database.NewQuery[models.User](db).WhereNotExists(subquery)
		sqlNotExists := qNotExists.ToSQL()
		assert.Contains(t, sqlNotExists, `NOT EXISTS (SELECT * FROM "accounts" WHERE (accounts.user_id = u.id))`)
	})

	t.Run("WhereHas and WhereDoesntHave wrap dynamic builder closures", func(t *testing.T) {
		qHas := database.NewQuery[models.User](db).WhereHas(func(idb bun.IDB) *bun.SelectQuery {
			return idb.NewSelect().Table("verifications").Where("verifications.user_id = u.id")
		})
		assert.Contains(t, qHas.ToSQL(), `EXISTS (SELECT * FROM "verifications" WHERE (verifications.user_id = u.id))`)

		qDoesntHave := database.NewQuery[models.User](db).WhereDoesntHave(func(idb bun.IDB) *bun.SelectQuery {
			return idb.NewSelect().Table("verifications").Where("verifications.user_id = u.id")
		})
		assert.Contains(t, qDoesntHave.ToSQL(), `NOT EXISTS (SELECT * FROM "verifications" WHERE (verifications.user_id = u.id))`)

		// nil builderFn 安全跳过
		qNilHas := database.NewQuery[models.User](db).WhereHas(nil)
		assert.NotContains(t, qNilHas.ToSQL(), "EXISTS")

		qNilDoesntHave := database.NewQuery[models.User](db).WhereDoesntHave(nil)
		assert.NotContains(t, qNilDoesntHave.ToSQL(), "NOT EXISTS")
	})
}

// TestActiveRecord_Candidate2_AggregatesAndScalars 验证通用聚合与单列标量值提取（Aggregate, Sum, Avg, Min, Max, Value, Pluck）。
func TestActiveRecord_Candidate2_AggregatesAndScalars(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("Aggregate, Sum, Avg, Min, Max query clauses", func(t *testing.T) {
		q := database.NewQuery[models.User](db).Where("status = ?", "active")

		// 验证 BuildSelect 结合 ColumnExpr 生成的聚合 SQL 语法
		sumSQL := q.BuildSelect().ColumnExpr("COALESCE(SUM(age), 0)").String()
		assert.Contains(t, sumSQL, "SELECT COALESCE(SUM(age), 0) FROM")
		assert.Contains(t, sumSQL, `WHERE (status = 'active') AND ("u".deleted_at IS NULL)`)

		avgSQL := q.BuildSelect().ColumnExpr("COALESCE(AVG(age), 0)").String()
		assert.Contains(t, avgSQL, "SELECT COALESCE(AVG(age), 0) FROM")
		assert.Contains(t, avgSQL, `WHERE (status = 'active') AND ("u".deleted_at IS NULL)`)

		minSQL := q.BuildSelect().ColumnExpr("MIN(age)").String()
		assert.Contains(t, minSQL, "SELECT MIN(age) FROM")
		assert.Contains(t, minSQL, `WHERE (status = 'active') AND ("u".deleted_at IS NULL)`)

		maxSQL := q.BuildSelect().ColumnExpr("MAX(age)").String()
		assert.Contains(t, maxSQL, "SELECT MAX(age) FROM")
		assert.Contains(t, maxSQL, `WHERE (status = 'active') AND ("u".deleted_at IS NULL)`)
	})

	t.Run("Value and Pluck query building clauses", func(t *testing.T) {
		q := database.NewQuery[models.User](db).Where("status = ?", "active")

		valSQL := q.BuildSelect().Column("email").Limit(1).String()
		assert.Contains(t, valSQL, `SELECT "u"."email" FROM "user" AS "u"`)
		assert.Contains(t, valSQL, "LIMIT 1")
		assert.Contains(t, valSQL, `WHERE (status = 'active') AND ("u".deleted_at IS NULL)`)

		pluckSQL := q.BuildSelect().Column("id").String()
		assert.Contains(t, pluckSQL, `SELECT "u"."id" FROM "user" AS "u"`)
		assert.Contains(t, pluckSQL, `WHERE (status = 'active') AND ("u".deleted_at IS NULL)`)
	})
}

// TestActiveRecord_Candidate3_FluentIntrospectionAndClones 验证流式内省、条件执行与独立深度克隆（Unless, Tap, Clone, ToSQL）。
func TestActiveRecord_Candidate3_FluentIntrospectionAndClones(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("Unless applies branch when condition is false", func(t *testing.T) {
		q1 := database.NewQuery[models.User](db).Unless(false, func(q *database.Query[models.User]) *database.Query[models.User] {
			return q.Where("status = ?", "active")
		})
		assert.Contains(t, q1.ToSQL(), "status = 'active'")

		q2 := database.NewQuery[models.User](db).Unless(true, func(q *database.Query[models.User]) *database.Query[models.User] {
			return q.Where("status = ?", "active")
		})
		assert.NotContains(t, q2.ToSQL(), "status = 'active'")

		q3 := database.NewQuery[models.User](db).Unless(true, func(q *database.Query[models.User]) *database.Query[models.User] {
			return q.Where("status = ?", "active")
		}, func(q *database.Query[models.User]) *database.Query[models.User] {
			return q.Where("status = ?", "fallback")
		})
		assert.Contains(t, q3.ToSQL(), "status = 'fallback'")
	})

	t.Run("Tap inspects query without breaking chain", func(t *testing.T) {
		var intermediateSQL string
		q := database.NewQuery[models.User](db).
			Where("role = ?", "admin").
			Tap(func(sub *database.Query[models.User]) {
				intermediateSQL = sub.ToSQL()
			}).
			Where("status = ?", "active")

		assert.Contains(t, intermediateSQL, "role = 'admin'")
		assert.NotContains(t, intermediateSQL, "status = 'active'")
		assert.Contains(t, q.ToSQL(), "status = 'active'")

		// nil fn 安全跳过
		assert.NotNil(t, q.Tap(nil))
	})

	t.Run("Clone creates deep independent copy of query", func(t *testing.T) {
		baseQ := database.NewQuery[models.User](db).Where("status = ?", "active")
		fork1 := baseQ.Clone().Where("role = ?", "admin")
		fork2 := baseQ.Clone().Where("role = ?", "user")

		assert.Contains(t, fork1.ToSQL(), "role = 'admin'")
		assert.NotContains(t, fork1.ToSQL(), "role = 'user'")

		assert.Contains(t, fork2.ToSQL(), "role = 'user'")
		assert.NotContains(t, fork2.ToSQL(), "role = 'admin'")

		// 原 baseQ 未受派生分支污染
		assert.NotContains(t, baseQ.ToSQL(), "role = 'admin'")
		assert.NotContains(t, baseQ.ToSQL(), "role = 'user'")
	})

	t.Run("ToSQL outputs formatted SQL string", func(t *testing.T) {
		sql := database.NewQuery[models.User](db).Where("id = ?", "u-99").ToSQL()
		assert.Contains(t, sql, `WHERE (id = 'u-99')`)
		assert.Contains(t, sql, `deleted_at IS NULL`)
	})
}

// trackableTestItem 声明支持变更追踪的测试模型。
type trackableTestItem struct {
	database.Base
	database.ChangeTracker
	Email string `bun:"email"`
	Name  string `bun:"name"`
	Role  string `bun:"role"`
}

func (m *trackableTestItem) SyncOriginal() {
	m.SyncOriginalFrom(m)
}

func (m *trackableTestItem) GetDirty() []string {
	return m.GetDirtyWith(m)
}

func (m *trackableTestItem) IsDirty(columns ...string) bool {
	return m.IsDirtyWith(m, columns...)
}

// TestActiveRecord_Candidate4_DirtyTracking 验证脏检查与差异化保存机制（SyncOriginal, IsDirty, GetDirty, Short-Circuit Save）。
func TestActiveRecord_Candidate4_DirtyTracking(t *testing.T) {
	ctx := context.Background()
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("ChangeTracker detects modifications accurately", func(t *testing.T) {
		item := &trackableTestItem{
			Base:  database.Base{ID: "usr-42"},
			Email: "alice@example.com",
			Name:  "Alice",
			Role:  "admin",
		}
		item.SyncOriginal()

		// 初始状态下无脏字段
		assert.False(t, item.IsDirty())
		assert.Empty(t, item.GetDirty())
		assert.False(t, item.IsDirty("email"))
		assert.False(t, item.IsDirty("name"))

		// 修改单一字段
		item.Name = "Alice In Wonderland"
		assert.True(t, item.IsDirty())
		assert.True(t, item.IsDirty("name"))
		assert.False(t, item.IsDirty("email"))
		assert.ElementsMatch(t, []string{"name"}, item.GetDirty())

		// 修改第二个字段
		item.Role = "superadmin"
		assert.True(t, item.IsDirty("role"))
		assert.True(t, item.IsDirty("name", "role"))
		assert.ElementsMatch(t, []string{"name", "role"}, item.GetDirty())

		// 同步基准快照后恢复干净状态
		item.SyncOriginal()
		assert.False(t, item.IsDirty())
		assert.Empty(t, item.GetDirty())
	})

	t.Run("Save short-circuits on clean DirtyTrackable model", func(t *testing.T) {
		item := &trackableTestItem{
			Base:  database.Base{ID: "usr-clean"},
			Email: "clean@example.com",
			Name:  "Clean",
		}
		item.SyncOriginal()

		// 未修改任何字段，Save 判定无脏字段短路返回 nil，不发起任何数据库 I/O
		err := database.Save(ctx, db, item)
		require.NoError(t, err)

		// 标记修改后，验证已检测到脏字段，此时 Save 无法短路
		item.Name = "Modified"
		assert.True(t, item.IsDirty("name"))
		assert.ElementsMatch(t, []string{"name"}, item.GetDirty())
	})

	t.Run("ChangeTracker edge cases handling", func(t *testing.T) {
		var tracker database.ChangeTracker
		// 未初始化原始快照时
		assert.Nil(t, tracker.GetDirtyWith(nil))
		assert.False(t, tracker.IsDirtyWith(nil))

		// 传入非法类型安全不崩溃
		tracker.SyncOriginalFrom(nil)
		tracker.SyncOriginalFrom(12345)
		assert.Nil(t, tracker.GetDirtyWith("not-a-struct"))
	})
}

// hookTestBatchItem 声明用于测试批量操作与生命周期钩子的测试模型。
type hookTestBatchItem struct {
	database.Base
	database.ChangeTracker
	Name             string `bun:"name"`
	FailBeforeCreate bool   `bun:"-"`
	CreatedHookRun   bool   `bun:"-"`
}

func (h *hookTestBatchItem) BeforeCreate(ctx context.Context) error {
	if h.FailBeforeCreate {
		return errors.New("before create validation failed")
	}
	return nil
}

func (h *hookTestBatchItem) AfterCreate(ctx context.Context) error {
	h.CreatedHookRun = true
	return nil
}

func (h *hookTestBatchItem) SyncOriginal() {
	h.SyncOriginalFrom(h)
}

func (h *hookTestBatchItem) GetDirty() []string {
	return h.GetDirtyWith(h)
}

func (h *hookTestBatchItem) IsDirty(columns ...string) bool {
	return h.IsDirtyWith(h, columns...)
}

// TestActiveRecord_Candidate1_BatchAndConflicts 校验 Candidate 1 批量写入、Upsert 与忽略冲突处理（CreateMany, Upsert, InsertOrIgnore）。
func TestActiveRecord_Candidate1_BatchAndConflicts(t *testing.T) {
	ctx := context.Background()
	db := bun.NewDB(nil, pgdialect.New())

	u1Name := "u1"
	u2Name := "u2"
	u1 := testutil.User().WithID("u-1").WithEmail("u1@example.com").WithName("u1").Build()
	u1.Username = &u1Name
	u2 := testutil.User().WithID("u-2").WithEmail("u2@example.com").WithName("u2").Build()
	u2.Username = &u2Name

	t.Run("BuildInsert produces valid multi-row VALUES SQL", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		ins := q.BuildInsert([]*models.User{u1, u2})
		sql := ins.String()

		assert.Contains(t, sql, `INSERT INTO "user"`)
		assert.Contains(t, sql, `'u1@example.com'`)
		assert.Contains(t, sql, `'u2@example.com'`)
		assert.Contains(t, sql, `VALUES`)
	})

	t.Run("BuildUpsert produces ON CONFLICT DO UPDATE with explicit columns", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		ins := q.BuildUpsert([]*models.User{u1}, []string{"email"}, "username", "status")
		sql := ins.String()

		assert.Contains(t, sql, `INSERT INTO "user" AS "u"`)
		assert.Contains(t, sql, `ON CONFLICT ("email") DO UPDATE SET`)
		assert.Contains(t, sql, `"username" = EXCLUDED."username"`)
		assert.Contains(t, sql, `"status" = EXCLUDED."status"`)
	})

	t.Run("BuildUpsert with multiple conflict keys", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		ins := q.BuildUpsert([]*models.User{u1}, []string{"id", "email"}, "status")
		sql := ins.String()

		assert.Contains(t, sql, `ON CONFLICT ("id", "email") DO UPDATE SET`)
		assert.Contains(t, sql, `"status" = EXCLUDED."status"`)
	})

	t.Run("BuildUpsert with empty updateColumns defaults to excluded fields", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		ins := q.BuildUpsert([]*models.User{u1}, []string{"email"})
		sql := ins.String()

		assert.Contains(t, sql, `ON CONFLICT ("email") DO UPDATE SET`)
		assert.Contains(t, sql, `EXCLUDED."username"`)
	})

	t.Run("BuildInsertOrIgnore produces ON CONFLICT DO NOTHING", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		ins := q.BuildInsertOrIgnore([]*models.User{u1, u2})
		sql := ins.String()

		assert.Contains(t, sql, `INSERT INTO "user" AS "u"`)
		assert.Contains(t, sql, `ON CONFLICT DO NOTHING`)
	})

	t.Run("Empty items return nil without database execution", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		// Query receiver methods
		require.NoError(t, q.CreateMany(ctx, nil))
		require.NoError(t, q.CreateMany(ctx, []*models.User{}))
		require.NoError(t, q.Upsert(ctx, nil, []string{"id"}))
		require.NoError(t, q.Upsert(ctx, []*models.User{}, []string{"id"}))
		require.NoError(t, q.InsertOrIgnore(ctx, nil))
		require.NoError(t, q.InsertOrIgnore(ctx, []*models.User{}))

		// Top-level persistence functions
		require.NoError(t, database.InsertMany(ctx, db, []*models.User{}))
		require.NoError(t, database.Upsert(ctx, db, []*models.User{}, []string{"id"}))
		require.NoError(t, database.InsertOrIgnore(ctx, db, []*models.User{}))
	})

	t.Run("Upsert validates conflictColumns non-empty", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		err := q.Upsert(ctx, []*models.User{u1}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "upsert requires at least one conflict column")

		err = database.Upsert(ctx, db, []*models.User{u1}, []string{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "upsert requires at least one conflict column")
	})

	t.Run("Batch operations trigger BeforeCreate and abort on failure", func(t *testing.T) {
		failItem := &hookTestBatchItem{FailBeforeCreate: true}

		err := database.InsertMany(ctx, db, []*hookTestBatchItem{failItem})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "before create validation failed")

		err = database.Upsert(ctx, db, []*hookTestBatchItem{failItem}, []string{"id"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "before create validation failed")

		err = database.InsertOrIgnore(ctx, db, []*hookTestBatchItem{failItem})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "before create validation failed")
	})
}

// TestActiveRecord_Candidate2_AtomicCountersAndRichClauses 校验 Candidate 2 原子计数器与富查询子句。
func TestActiveRecord_Candidate2_AtomicCountersAndRichClauses(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("BuildIncrement produces atomic column addition with subquery scope and updated_at", func(t *testing.T) {
		q := database.NewQuery[models.User](db).Where("status = ?", "active")
		upd := q.BuildIncrement("ban_count", 1)
		sql := upd.String()

		assert.Contains(t, sql, `UPDATE "user" AS "u"`)
		assert.Contains(t, sql, `SET "ban_count" = "ban_count" + 1`)
		assert.Contains(t, sql, `updated_at = `)
		assert.Contains(t, sql, `"u".id IN (SELECT "u"."id" FROM "user" AS "u"`)
		assert.Contains(t, sql, `status = 'active'`)
		assert.Contains(t, sql, `"u".deleted_at IS NULL`)
	})

	t.Run("BuildIncrement with custom delta", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		upd := q.BuildIncrement("ban_count", 5)
		sql := upd.String()

		assert.Contains(t, sql, `SET "ban_count" = "ban_count" + 5`)
	})

	t.Run("BuildDecrement produces atomic column subtraction with subquery scope", func(t *testing.T) {
		q := database.NewQuery[models.User](db).Where("role = ?", "user")
		upd := q.BuildDecrement("ban_count", 3)
		sql := upd.String()

		assert.Contains(t, sql, `UPDATE "user" AS "u"`)
		assert.Contains(t, sql, `SET "ban_count" = "ban_count" - 3`)
		assert.Contains(t, sql, `"u".id IN (SELECT "u"."id" FROM "user" AS "u"`)
		assert.Contains(t, sql, `role = 'user'`)
		assert.Contains(t, sql, `"u".deleted_at IS NULL`)
	})

	t.Run("WhereBetween generates BETWEEN condition", func(t *testing.T) {
		sql := database.NewQuery[models.User](db).WhereBetween("created_at", "2026-01-01", "2026-12-31").ToSQL()
		assert.Contains(t, sql, `WHERE (created_at BETWEEN '2026-01-01' AND '2026-12-31')`)
	})

	t.Run("WhereNotBetween generates NOT BETWEEN condition", func(t *testing.T) {
		sql := database.NewQuery[models.User](db).WhereNotBetween("created_at", "2026-01-01", "2026-12-31").ToSQL()
		assert.Contains(t, sql, `WHERE (created_at NOT BETWEEN '2026-01-01' AND '2026-12-31')`)
	})

	t.Run("WhereColumn generates column-to-column comparison safely", func(t *testing.T) {
		sql := database.NewQuery[models.User](db).WhereColumn("updated_at", ">", "created_at").ToSQL()
		assert.Contains(t, sql, `WHERE ("updated_at" > "created_at")`)
	})

	t.Run("WhereILike generates ILIKE query", func(t *testing.T) {
		sql := database.NewQuery[models.User](db).WhereILike("email", "%@example.com").ToSQL()
		assert.Contains(t, sql, `WHERE (email ILIKE '%@example.com')`)
	})

	t.Run("WhereNot generates NOT condition", func(t *testing.T) {
		sql := database.NewQuery[models.User](db).WhereNot("status = ?", "banned").ToSQL()
		assert.Contains(t, sql, `WHERE (NOT (status = 'banned'))`)
	})
}

// TestActiveRecord_Candidate3_CursorStreamingAndLookups 校验 Candidate 3 游标、批量查找、首条查找与表截断。
func TestActiveRecord_Candidate3_CursorStreamingAndLookups(t *testing.T) {
	ctx := context.Background()
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("BuildTruncate generates TRUNCATE TABLE statement", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		b, err := q.BuildTruncate().AppendQuery(schema.NewQueryGen(db.Dialect()), nil)
		require.NoError(t, err)
		assert.Contains(t, string(b), `TRUNCATE TABLE "user"`)
	})

	t.Run("FindMany returns empty slice on empty IDs", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		items, err := q.FindMany(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, items)

		items, err = q.FindMany(ctx, []string{})
		require.NoError(t, err)
		assert.Empty(t, items)
	})

	t.Run("FirstWhere builds query with 1, 2, or 0 args", func(t *testing.T) {
		// 1 参数：column = val
		q1 := database.NewQuery[models.User](db)
		_ = q1.Where("email = ?", "alice@example.com")
		assert.Contains(t, q1.ToSQL(), `email = 'alice@example.com'`)

		// 2 参数：column op val
		q2 := database.NewQuery[models.User](db)
		_ = q2.Where("login_count >= ?", 10)
		assert.Contains(t, q2.ToSQL(), `login_count >= 10`)

		// 0 参数：column 表达式
		q3 := database.NewQuery[models.User](db)
		_ = q3.Where("banned")
		assert.Contains(t, q3.ToSQL(), `WHERE (banned)`)
	})

	t.Run("Cursor streaming requires valid *bun.DB instance", func(t *testing.T) {
		// 构造缺失 baseDB 的自定义 Query
		customQ := &database.Query[models.User]{}
		err := customQ.Cursor(ctx, func(u *models.User) error { return nil })
		require.Error(t, err)
	})
}

// TestActiveRecord_EloquentAdvancedQueryBuilder 验证对标 Laravel 11/12/13 的高级查询构建器方法生成正确的 SQL。
func TestActiveRecord_EloquentAdvancedQueryBuilder(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("Select and SelectExpr", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			Select("id", "email").
			SelectExpr("COUNT(*) AS total")
		sql := q.ToSQL()
		assert.Contains(t, sql, `"id"`)
		assert.Contains(t, sql, `"email"`)
		assert.Contains(t, sql, `COUNT(*) AS total`)
	})

	t.Run("Distinct and DistinctOn", func(t *testing.T) {
		q := database.NewQuery[models.User](db).Distinct()
		assert.Contains(t, q.ToSQL(), "DISTINCT")

		q2 := database.NewQuery[models.User](db).DistinctOn("email")
		assert.Contains(t, q2.ToSQL(), `DISTINCT ON (email)`)
	})

	t.Run("GroupBy and Having", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			GroupBy("role").
			Having("count(*) > ?", 5)
		sql := q.ToSQL()
		assert.Contains(t, sql, `GROUP BY "role"`)
		assert.Contains(t, sql, `HAVING (count(*) > 5)`)
	})

	t.Run("WhereGroup and WhereOrGroup nested parentheses", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			Where("status = ?", "active").
			WhereGroup(func(sub *database.Query[models.User]) *database.Query[models.User] {
				return sub.Where("role = ?", "admin").WhereOr("role = ?", "superadmin")
			})
		sql := q.ToSQL()
		assert.Contains(t, sql, `status = 'active'`)
		assert.Contains(t, sql, `(role = 'admin') OR (role = 'superadmin')`)

		// nil safe
		qNil := database.NewQuery[models.User](db).WhereGroup(nil).WhereOrGroup(nil)
		assert.NotNil(t, qNil)

		qOr := database.NewQuery[models.User](db).
			Where("status = ?", "banned").
			WhereOrGroup(func(sub *database.Query[models.User]) *database.Query[models.User] {
				return sub.Where("role = ?", "tester")
			})
		assert.Contains(t, sql, `status = 'active'`)
		assert.Contains(t, qOr.ToSQL(), `status = 'banned'`)
		assert.Contains(t, qOr.ToSQL(), `role = 'tester'`)
	})

	t.Run("WhereAny and WhereAll (Laravel 11+)", func(t *testing.T) {
		qAny := database.NewQuery[models.User](db).
			WhereAny([]string{"name", "email"}, "ILIKE", "%admin%")
		sqlAny := qAny.ToSQL()
		assert.Contains(t, sqlAny, `(name ILIKE '%admin%') OR (email ILIKE '%admin%')`)

		// Empty columns short-circuit
		qAnyEmpty := database.NewQuery[models.User](db).WhereAny(nil, "=", 1)
		assert.NotContains(t, qAnyEmpty.ToSQL(), "=")

		qAll := database.NewQuery[models.User](db).
			WhereAll([]string{"status", "role"}, "=", "active")
		sqlAll := qAll.ToSQL()
		assert.Contains(t, sqlAll, `(status = 'active') AND (role = 'active')`)

		qAllEmpty := database.NewQuery[models.User](db).WhereAll(nil, "=", 1)
		assert.NotContains(t, qAllEmpty.ToSQL(), "=")
	})

	t.Run("Date and Time where helpers", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			WhereDate("created_at", "2026-10-01").
			WhereYear("created_at", 2026).
			WhereMonth("created_at", 10).
			WhereDay("created_at", 1)
		sql := q.ToSQL()
		assert.Contains(t, sql, `DATE(created_at) = '2026-10-01'`)
		assert.Contains(t, sql, `EXTRACT(YEAR FROM created_at) = 2026`)
		assert.Contains(t, sql, `EXTRACT(MONTH FROM created_at) = 10`)
		assert.Contains(t, sql, `EXTRACT(DAY FROM created_at) = 1`)
	})

	t.Run("JSONB helpers WhereJSONContains and WhereJSONLength", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			WhereJSONContains("metadata", `{"verified":true}`).
			WhereJSONLength("tags", ">", 0)
		sql := q.ToSQL()
		assert.Contains(t, sql, `metadata @> '{"verified":true}'::jsonb`)
		assert.Contains(t, sql, `jsonb_array_length(tags) > 0`)
	})

	t.Run("Ordering helpers Latest Oldest InRandomOrder", func(t *testing.T) {
		q1 := database.NewQuery[models.User](db).Latest()
		assert.Contains(t, q1.ToSQL(), `"u"."created_at" DESC`)

		q2 := database.NewQuery[models.User](db).Latest("updated_at")
		assert.Contains(t, q2.ToSQL(), `"u"."updated_at" DESC`)

		q3 := database.NewQuery[models.User](db).Oldest()
		assert.Contains(t, q3.ToSQL(), `"u"."created_at" ASC`)

		q4 := database.NewQuery[models.User](db).Oldest("published_at")
		assert.Contains(t, q4.ToSQL(), `"u"."published_at" ASC`)

		q5 := database.NewQuery[models.User](db).InRandomOrder()
		assert.Contains(t, q5.ToSQL(), `ORDER BY RANDOM()`)
	})

	t.Run("WithCount and WithoutTrashed and IsTrashed", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			WithCount("accounts", "user_id").
			WithTrashed().
			WithoutTrashed()
		sql := q.ToSQL()
		assert.Contains(t, sql, `(SELECT count(*) FROM accounts WHERE accounts.user_id = "u".id) AS "accounts_count"`)
		assert.Contains(t, sql, `deleted_at IS NULL`)

		var u models.User
		assert.False(t, database.IsTrashed(&u))
		now := time.Now().UTC()
		u.SetDeletedAt(&now)
		assert.True(t, database.IsTrashed(&u))
		assert.False(t, database.IsTrashed(nil))
	})
}

// TestActiveRecord_EloquentV13_QueryBuilder 验证最新 Laravel 13 风格的查询构造器增强特性（N+1消除、子查询、CTE、分组与集合运算）。
func TestActiveRecord_EloquentV13_QueryBuilder(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("SelectRaw and SelectSub projection", func(t *testing.T) {
		sub := db.NewSelect().Table("orders").ColumnExpr("MAX(created_at)").Where("user_id = ?TableAlias.id")
		q := database.NewQuery[models.User](db).
			SelectRaw("COALESCE(name, 'Anonymous') AS display_name").
			SelectSub(sub, "latest_order_at")
		sql := q.ToSQL()
		assert.Contains(t, sql, `COALESCE(name, 'Anonymous') AS display_name`)
		assert.Contains(t, sql, `AS "latest_order_at"`)

		// nil/empty safety
		qNil := database.NewQuery[models.User](db).SelectSub(nil, "alias").SelectSub(sub, "")
		assert.NotContains(t, qNil.ToSQL(), `AS "alias"`)
	})

	t.Run("GroupByExpr, HavingOr, HavingBetween, HavingRaw", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			GroupBy("role").
			GroupByExpr("DATE_TRUNC('month', created_at)").
			Having("count(*) > ?", 10).
			HavingOr("count(*) = ?", 0).
			HavingBetween("total_spent", 100, 500).
			HavingRaw("AVG(score) >= ?", 80)
		sql := q.ToSQL()
		assert.Contains(t, sql, `GROUP BY "role", DATE_TRUNC('month', created_at)`)
		assert.Contains(t, sql, `HAVING (count(*) > 10)`)
		assert.Contains(t, sql, `OR count(*) = 0`)
		assert.Contains(t, sql, `"total_spent" BETWEEN 100 AND 500`)
		assert.Contains(t, sql, `AVG(score) >= 80`)

		// empty GroupByExpr safety
		qEmptyGroup := database.NewQuery[models.User](db).GroupByExpr("")
		assert.NotContains(t, qEmptyGroup.ToSQL(), "GROUP BY")
	})

	t.Run("Eager loading WithRelations and Relation Aggregates", func(t *testing.T) {
		type dummyAccount struct {
			bun.BaseModel `bun:"table:accounts,alias:a"`
			ID            string `bun:"id,pk"`
			UserID        string `bun:"user_id"`
		}
		type dummyUserWithRel struct {
			bun.BaseModel `bun:"table:users,alias:u"`
			database.Base
			Accounts []*dummyAccount `bun:"rel:has-many,join:id=user_id"`
		}

		qRel := database.NewQuery[dummyUserWithRel](db).
			WithRelations("Accounts", "")
		assert.NotNil(t, qRel)

		q := database.NewQuery[models.User](db).
			WithCount("accounts", "user_id", "custom_accounts_count").
			WithSum("orders", "user_id", "amount").
			WithSum("orders", "user_id", "amount", "total_orders_sum").
			WithAvg("ratings", "user_id", "stars").
			WithAvg("ratings", "user_id", "stars", "avg_stars").
			WithMin("logs", "user_id", "latency").
			WithMin("logs", "user_id", "latency", "min_latency").
			WithMax("scores", "user_id", "points").
			WithMax("scores", "user_id", "points", "max_points")
		sql := q.ToSQL()
		assert.Contains(t, sql, `AS "custom_accounts_count"`)
		assert.Contains(t, sql, `AS "orders_amount_sum"`)
		assert.Contains(t, sql, `AS "total_orders_sum"`)
		assert.Contains(t, sql, `AS "ratings_stars_avg"`)
		assert.Contains(t, sql, `AS "avg_stars"`)
		assert.Contains(t, sql, `AS "logs_latency_min"`)
		assert.Contains(t, sql, `AS "min_latency"`)
		assert.Contains(t, sql, `AS "scores_points_max"`)
		assert.Contains(t, sql, `AS "max_points"`)
	})

	t.Run("Has, OrHas, DoesntHave, OrDoesntHave", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			Has("accounts", "user_id", ">=", 2).
			OrHas("verifications", "user_id", ">", 0).
			DoesntHave("violations", "user_id").
			OrDoesntHave("warnings", "user_id")
		sql := q.ToSQL()
		assert.Contains(t, sql, `(SELECT count(*) FROM accounts WHERE accounts.user_id = "u".id) >= 2`)
		assert.Contains(t, sql, `(SELECT count(*) FROM verifications WHERE verifications.user_id = "u".id) > 0`)
		assert.Contains(t, sql, `(SELECT count(*) FROM violations WHERE violations.user_id = "u".id) < 1`)
		assert.Contains(t, sql, `(SELECT count(*) FROM warnings WHERE warnings.user_id = "u".id) < 1`)

		// Operator whitelisting fallback and identifier sanitization
		qSafe := database.NewQuery[models.User](db).
			Has("accounts; DROP TABLE", "user_id;--", "INVALID_OP", 3)
		safeSQL := qSafe.ToSQL()
		assert.Contains(t, safeSQL, `(SELECT count(*) FROM accountsDROPTABLE WHERE accountsDROPTABLE.user_id = "u".id) >= 3`)
	})

	t.Run("WhereHasRelation, OrWhereHasRelation, WhereDoesntHaveRelation, OrWhereDoesntHaveRelation", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			WhereHasRelation("accounts", "user_id", func(sub *bun.SelectQuery) *bun.SelectQuery {
				return sub.Where("provider = ?", "google")
			}).
			OrWhereHasRelation("verifications", "user_id").
			WhereDoesntHaveRelation("banned_records", "user_id", func(sub *bun.SelectQuery) *bun.SelectQuery {
				return sub.Where("active = true")
			}).
			OrWhereDoesntHaveRelation("strikes", "user_id")
		sql := q.ToSQL()
		assert.Contains(t, sql, `EXISTS (SELECT 1 FROM "accounts" WHERE (accounts.user_id = "u".id) AND (provider = 'google'))`)
		assert.Contains(t, sql, `OR (EXISTS (SELECT 1 FROM "verifications" WHERE (verifications.user_id = "u".id)))`)
		assert.Contains(t, sql, `NOT EXISTS (SELECT 1 FROM "banned_records" WHERE (banned_records.user_id = "u".id) AND (active = true))`)
		assert.Contains(t, sql, `OR (NOT EXISTS (SELECT 1 FROM "strikes" WHERE (strikes.user_id = "u".id)))`)

		q2 := database.NewQuery[models.User](db).
			WhereHasRelation("accounts", "user_id").
			OrWhereHasRelation("verifications", "user_id", func(sub *bun.SelectQuery) *bun.SelectQuery {
				return sub.Where("type = ?", "email")
			}).
			WhereDoesntHaveRelation("banned_records", "user_id").
			OrWhereDoesntHaveRelation("strikes", "user_id", func(sub *bun.SelectQuery) *bun.SelectQuery {
				return sub.Where("severity > 1")
			})
		sql2 := q2.ToSQL()
		assert.Contains(t, sql2, `type = 'email'`)
		assert.Contains(t, sql2, `severity > 1`)
	})

	t.Run("Subquery operators WhereSub, WhereInSub, WhereNotInSub and OR variants", func(t *testing.T) {
		subAvg := db.NewSelect().Table("orders").ColumnExpr("AVG(amount)")
		subUsers := db.NewSelect().Table("vip_users").Column("user_id")
		subBlocked := db.NewSelect().Table("blocked_users").Column("user_id")
		subExists := db.NewSelect().Table("active_sessions").ColumnExpr("1").Where("user_id = ?TableAlias.id")
		subNotExists := db.NewSelect().Table("pending_deletions").ColumnExpr("1").Where("user_id = ?TableAlias.id")

		q := database.NewQuery[models.User](db).
			WhereSub("balance", ">", subAvg).
			OrWhereSub("bonus", ">=", subAvg).
			WhereInSub("id", subUsers).
			OrWhereInSub("id", subUsers).
			WhereNotInSub("id", subBlocked).
			OrWhereNotInSub("id", subBlocked).
			OrWhereExists(subExists).
			OrWhereNotExists(subNotExists)
		sql := q.ToSQL()
		assert.Contains(t, sql, `"balance" > (SELECT AVG(amount) FROM "orders")`)
		assert.Contains(t, sql, `OR ("bonus" >= (SELECT AVG(amount) FROM "orders"))`)
		assert.Contains(t, sql, `"id" IN (SELECT "user_id" FROM "vip_users")`)
		assert.Contains(t, sql, `OR ("id" IN (SELECT "user_id" FROM "vip_users"))`)
		assert.Contains(t, sql, `"id" NOT IN (SELECT "user_id" FROM "blocked_users")`)
		assert.Contains(t, sql, `OR ("id" NOT IN (SELECT "user_id" FROM "blocked_users"))`)
		assert.Contains(t, sql, `OR (EXISTS (SELECT 1 FROM "active_sessions"`)
		assert.Contains(t, sql, `OR (NOT EXISTS (SELECT 1 FROM "pending_deletions"`)

		// nil subquery safety
		qNil := database.NewQuery[models.User](db).
			WhereSub("balance", ">", nil).
			OrWhereSub("bonus", ">=", nil).
			WhereInSub("id", nil).
			OrWhereInSub("id", nil).
			WhereNotInSub("id", nil).
			OrWhereNotInSub("id", nil).
			OrWhereExists(nil).
			OrWhereNotExists(nil)
		assert.NotContains(t, qNil.ToSQL(), "balance")
	})

	t.Run("FromSub, WithCTE, Union and UnionAll", func(t *testing.T) {
		subDerived := db.NewSelect().Table("users").Where("status = ?", "active")
		cteQuery := db.NewSelect().Table("accounts").Where("provider = ?", "github")
		unionQuery := db.NewSelect().Table("users").Where("role = ?", "admin")
		unionAllQuery := db.NewSelect().Table("users").Where("role = ?", "superadmin")

		q := database.NewQuery[models.User](db).
			WithCTE("github_accounts", cteQuery).
			FromSub(subDerived, "active_users").
			Union(unionQuery).
			UnionAll(unionAllQuery)
		sql := q.ToSQL()
		assert.Contains(t, sql, `WITH "github_accounts" AS (SELECT * FROM "accounts" WHERE (provider = 'github'))`)
		assert.Contains(t, sql, `(SELECT * FROM "users" WHERE (status = 'active')) AS "active_users"`)
		assert.Contains(t, sql, `UNION (SELECT * FROM "users" WHERE (role = 'admin'))`)
		assert.Contains(t, sql, `UNION ALL (SELECT * FROM "users" WHERE (role = 'superadmin'))`)

		// nil safety
		qNil := database.NewQuery[models.User](db).
			WithCTE("", nil).
			FromSub(nil, "").
			Union(nil).
			UnionAll(nil)
		assert.NotNil(t, qNil)
	})

	t.Run("WhereRow tuple comparison", func(t *testing.T) {
		q := database.NewQuery[models.User](db).
			WhereRow([]string{"created_at", "id"}, "<", "2026-10-01T00:00:00Z", "usr-123")
		sql := q.ToSQL()
		assert.Contains(t, sql, `("created_at", "id") < ('2026-10-01T00:00:00Z', 'usr-123')`)

		// empty safety
		qEmptyCols := database.NewQuery[models.User](db).WhereRow(nil, "<", 1)
		assert.NotContains(t, qEmptyCols.ToSQL(), "<")
		qEmptyVals := database.NewQuery[models.User](db).WhereRow([]string{"id"}, "<")
		assert.NotContains(t, qEmptyVals.ToSQL(), "<")
	})
}

// TestActiveRecord_CursorAndSimplePagination 验证游标分页编解码与简易分页机制。
func TestActiveRecord_CursorAndSimplePagination(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	t.Run("EncodeCursor and DecodeCursor roundtrip", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		id := "usr-999"

		token := database.EncodeCursor(now, id)
		assert.NotEmpty(t, token)

		decodedTime, decodedID, ok := database.DecodeCursor(token)
		require.True(t, ok)
		assert.Equal(t, id, decodedID)
		assert.True(t, now.Equal(decodedTime))
	})

	t.Run("DecodeCursor invalid input", func(t *testing.T) {
		_, _, ok := database.DecodeCursor("")
		assert.False(t, ok)

		_, _, ok = database.DecodeCursor("invalid-base64!!!")
		assert.False(t, ok)

		_, _, ok = database.DecodeCursor("e30=") // empty json {}
		assert.False(t, ok)

		// Zero time or empty id
		zeroTimeToken := database.EncodeCursor(time.Time{}, "usr-1")
		assert.Empty(t, zeroTimeToken)

		emptyIDToken := database.EncodeCursor(time.Now(), "")
		assert.Empty(t, emptyIDToken)
	})

	t.Run("Base implements Timestamped interface", func(t *testing.T) {
		now := time.Now().UTC()
		base := database.Base{
			ID:        "usr-abc",
			CreatedAt: now,
			UpdatedAt: now,
		}
		assert.Equal(t, now, base.GetCreatedAt())
		assert.Equal(t, now, base.GetUpdatedAt())

		var ts database.Timestamped = &base
		assert.Equal(t, "usr-abc", ts.GetID())
		assert.Equal(t, now, ts.GetCreatedAt())

		var nilBase *database.Base
		assert.True(t, nilBase.GetCreatedAt().IsZero())
		assert.True(t, nilBase.GetUpdatedAt().IsZero())
	})

	t.Run("CursorPaginate SQL construction with cursor", func(t *testing.T) {
		now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		cursor := database.EncodeCursor(now, "usr-seek-123")

		q := database.NewQuery[models.User](db)
		if tVal, idVal, ok := database.DecodeCursor(cursor); ok {
			q = q.WhereRow([]string{"created_at", "id"}, "<", tVal, idVal)
		}
		q = q.OrderExpr("?TableAlias.? DESC, ?TableAlias.? DESC", bun.Ident("created_at"), bun.Ident("id")).Limit(21)
		sql := q.ToSQL()

		assert.Contains(t, sql, `("created_at", "id") < ('2026-10-01 12:00:00+00:00', 'usr-seek-123')`)
		assert.Contains(t, sql, `ORDER BY "u"."created_at" DESC, "u"."id" DESC`)
		assert.Contains(t, sql, `LIMIT 21`)
	})

	t.Run("SimplePaginate boundary calculation", func(t *testing.T) {
		q := database.NewQuery[models.User](db).Offset(0).Limit(21)
		sql := q.ToSQL()
		assert.Contains(t, sql, "LIMIT 21")
	})
}

// TestActiveRecord_PivotOperations 验证多对多关联中间表操作契约与边界安全。
func TestActiveRecord_PivotOperations(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	ctx := context.Background()

	t.Run("AttachPivot empty parameter safety", func(t *testing.T) {
		assert.NoError(t, database.AttachPivot(ctx, db, "", "user_id", "role_id", "usr-1", "role-1"))
		assert.NoError(t, database.AttachPivot(ctx, db, "user_roles", "", "role_id", "usr-1", "role-1"))
		assert.NoError(t, database.AttachPivot(ctx, db, "user_roles", "user_id", "", "usr-1", "role-1"))
		assert.NoError(t, database.AttachPivot(ctx, db, "user_roles", "user_id", "role_id", "", "role-1"))
		assert.NoError(t, database.AttachPivot(ctx, db, "user_roles", "user_id", "role_id", "usr-1"))
		assert.NoError(t, database.AttachPivot(ctx, db, "user_roles", "user_id", "role_id", "usr-1", "", "   "))
	})

	t.Run("DetachPivot empty parameter safety", func(t *testing.T) {
		assert.NoError(t, database.DetachPivot(ctx, db, "", "user_id", "role_id", "usr-1"))
		assert.NoError(t, database.DetachPivot(ctx, db, "user_roles", "", "role_id", "usr-1"))
		assert.NoError(t, database.DetachPivot(ctx, db, "user_roles", "user_id", "", "usr-1"))
		assert.NoError(t, database.DetachPivot(ctx, db, "user_roles", "user_id", "role_id", ""))
	})

	t.Run("SyncPivot empty parameter safety", func(t *testing.T) {
		assert.NoError(t, database.SyncPivot(ctx, db, "", "user_id", "role_id", "usr-1", []string{"role-1"}))
		assert.NoError(t, database.SyncPivot(ctx, db, "user_roles", "", "role_id", "usr-1", []string{"role-1"}))
		assert.NoError(t, database.SyncPivot(ctx, db, "user_roles", "user_id", "", "usr-1", []string{"role-1"}))
		assert.NoError(t, database.SyncPivot(ctx, db, "user_roles", "user_id", "role_id", "", []string{"role-1"}))
	})

	t.Run("TogglePivot empty parameter safety", func(t *testing.T) {
		assert.NoError(t, database.TogglePivot(ctx, db, "", "user_id", "role_id", "usr-1", "role-1"))
		assert.NoError(t, database.TogglePivot(ctx, db, "user_roles", "", "role_id", "usr-1", "role-1"))
		assert.NoError(t, database.TogglePivot(ctx, db, "user_roles", "user_id", "", "usr-1", "role-1"))
		assert.NoError(t, database.TogglePivot(ctx, db, "user_roles", "user_id", "role_id", "", "role-1"))
		assert.NoError(t, database.TogglePivot(ctx, db, "user_roles", "user_id", "role_id", "usr-1"))
	})

	t.Run("Query receiver methods delegate properly", func(t *testing.T) {
		q := database.NewQuery[models.User](db)
		assert.NoError(t, q.AttachPivot(ctx, "", "u_id", "r_id", "1"))
		assert.NoError(t, q.DetachPivot(ctx, "", "u_id", "r_id", "1"))
		assert.NoError(t, q.SyncPivot(ctx, "", "u_id", "r_id", "1", nil))
		assert.NoError(t, q.TogglePivot(ctx, "", "u_id", "r_id", "1"))
	})
}
