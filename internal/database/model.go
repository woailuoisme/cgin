// Package database 负责 PostgreSQL 连接池管理、事务工作单元、Base 实体基座与通用 Active Record 泛型仓储引擎。
package database

import (
	"cmp"
	"context"
	"time"

	"github.com/dromara/carbon/v2"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/uptrace/bun"
)

// ============================================================================
// 1. 实体基座与钩子 (Base Model & Lifecycle Hooks)
// ============================================================================

// Base 提供三张表共用的主键与时间戳，并在写入前统一补齐。
type Base struct {
	ID        string    `bun:"id,pk"`
	CreatedAt time.Time `bun:"created_at"`
	UpdatedAt time.Time `bun:"updated_at"`
}

func toCarbon(t time.Time) *carbon.Carbon {
	if t.IsZero() {
		return carbon.NewCarbon()
	}
	return carbon.CreateFromStdTime(t)
}

func toCarbonPtr(t *time.Time) *carbon.Carbon {
	if t == nil {
		return carbon.NewCarbon()
	}
	return toCarbon(*t)
}

// CreatedAtCarbon 返回创建时间的 *carbon.Carbon 包装实例，便于业务层进行链式时间计算与相对时间展示。
func (b *Base) CreatedAtCarbon() *carbon.Carbon {
	if b == nil {
		return carbon.NewCarbon()
	}
	return toCarbon(b.CreatedAt)
}

// UpdatedAtCarbon 返回更新时间的 *carbon.Carbon 包装实例。
func (b *Base) UpdatedAtCarbon() *carbon.Carbon {
	if b == nil {
		return carbon.NewCarbon()
	}
	return toCarbon(b.UpdatedAt)
}

// UTCPtr 返回时间指针的 UTC 形式，nil 时返回 nil。
func UTCPtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := value.UTC()
	return &converted
}

// GetID 返回模型的主键 ID（满足 Model 契约）。
func (b *Base) GetID() string {
	if b == nil {
		return ""
	}
	return b.ID
}

// SetID 设置模型的主键 ID（满足 Model 契约）。
func (b *Base) SetID(id string) {
	if b != nil {
		b.ID = id
	}
}

// GetCreatedAt 返回模型的创建时间（满足 Timestamped 契约）。
func (b *Base) GetCreatedAt() time.Time {
	if b == nil {
		return time.Time{}
	}
	return b.CreatedAt
}

// GetUpdatedAt 返回模型的更新时间。
func (b *Base) GetUpdatedAt() time.Time {
	if b == nil {
		return time.Time{}
	}
	return b.UpdatedAt
}

// BeforeAppendModel 在写入前生成 UUIDv7 并刷新时间戳，数据库默认值仅作兜底。
func (b *Base) BeforeAppendModel(_ context.Context, query bun.Query) error {
	now := time.Now().UTC()

	switch query.(type) {
	case *bun.InsertQuery:
		if b.ID == "" {
			b.ID = uuid.Must(uuid.NewV7()).String()
		}
		b.CreatedAt = now
		b.UpdatedAt = now
	case *bun.UpdateQuery:
		b.UpdatedAt = now
	}
	return nil
}

// SoftDeletes 嵌入基座，为模型提供统一的软删除字段与便捷方法。
type SoftDeletes struct {
	DeletedAt *time.Time `bun:"deleted_at"`
}

// GetDeletedAt 返回软删除时间指针。
func (s *SoftDeletes) GetDeletedAt() *time.Time {
	if s == nil {
		return nil
	}
	return s.DeletedAt
}

// SetDeletedAt 设置软删除时间指针。
func (s *SoftDeletes) SetDeletedAt(t *time.Time) {
	if s != nil {
		s.DeletedAt = t
	}
}

// DeletedAtCarbon 返回软删除时间的 *carbon.Carbon 包装实例。
func (s *SoftDeletes) DeletedAtCarbon() *carbon.Carbon {
	if s == nil {
		return carbon.NewCarbon()
	}
	return toCarbonPtr(s.DeletedAt)
}

// ============================================================================
// 2. 核心抽象接口 (Core Interfaces)
// ============================================================================

// Model 约束挂载主键 ID 的实体模型。
type Model interface {
	GetID() string
	SetID(id string)
}

// Timestamped 约束拥有标准时间戳的实体模型。
type Timestamped interface {
	Model
	GetCreatedAt() time.Time
}

// SoftDeletable 约束支持软删除的实体模型。
type SoftDeletable interface {
	Model
	GetDeletedAt() *time.Time
	SetDeletedAt(t *time.Time)
}

// IsTrashed 判断软删除实体当前是否处于被软删除状态。
func IsTrashed(m SoftDeletable) bool {
	return m != nil && m.GetDeletedAt() != nil
}

// DeletedAtCarbon 从任意实现了 SoftDeletable 契约的模型中提取软删除时间的 *carbon.Carbon 包装实例。
func DeletedAtCarbon(m SoftDeletable) *carbon.Carbon {
	if m == nil {
		return carbon.NewCarbon()
	}
	return toCarbonPtr(m.GetDeletedAt())
}

// BeforeCreator 领域模型前置创建拦截契约。
type BeforeCreator interface {
	BeforeCreate(ctx context.Context) error
}

// AfterCreator 领域模型后置创建拦截契约。
type AfterCreator interface {
	AfterCreate(ctx context.Context) error
}

// BeforeUpdater 领域模型前置更新拦截契约。
type BeforeUpdater interface {
	BeforeUpdate(ctx context.Context) error
}

// AfterUpdater 领域模型后置更新拦截契约。
type AfterUpdater interface {
	AfterUpdate(ctx context.Context) error
}

// BeforeDeleter 领域模型前置删除拦截契约。
type BeforeDeleter interface {
	BeforeDelete(ctx context.Context) error
}

// AfterDeleter 领域模型后置删除拦截契约。
type AfterDeleter interface {
	AfterDelete(ctx context.Context) error
}

// ============================================================================
// 3. 上下文数据库与事务感知 (Context-Aware DB Seam)
// ============================================================================

type ctxDBKey struct{}

// WithDB 将活跃数据库连接或事务实例注入 Context。
func WithDB(ctx context.Context, db bun.IDB) context.Context {
	return context.WithValue(ctx, ctxDBKey{}, db)
}

// DBFromContext 从 Context 提取活跃数据库或事务连接；若未绑定则返回 nil。
func DBFromContext(ctx context.Context) bun.IDB {
	if db, ok := ctx.Value(ctxDBKey{}).(bun.IDB); ok && db != nil {
		return db
	}
	return nil
}

// ResolveDB 优先从 Context 提取活跃事务或连接，无活跃连接时回退至 fallback。
func ResolveDB(ctx context.Context, fallback bun.IDB) bun.IDB {
	if active := DBFromContext(ctx); active != nil {
		return active
	}
	return fallback
}

// ============================================================================
// 4. 链式查询器与一体化仓储核心 (Query Builder & Core Lifecycle)
// ============================================================================

// ScopeFunc 声明可复用的查询作用域函数。
type ScopeFunc[T any] func(*Query[T]) *Query[T]

// Query 封装 bun.SelectQuery，兼具流式链式查询、作用域（Scopes）与仓储（Repository）增删改查全能力。
type Query[T any] struct {
	db                bun.IDB
	baseDB            *bun.DB
	sel               *bun.SelectQuery
	withTrashed       bool
	onlyTrashed       bool
	chunkSize         int
	hasExplicitSelect bool
}

// UnwrapBunDB 尝试从 bun.IDB 实例中提取底层的 *bun.DB 实例（支持直接断言与 BunDB/Unwrap 接口解包）。
func UnwrapBunDB(idb bun.IDB) *bun.DB {
	switch v := idb.(type) {
	case *bun.DB:
		return v
	case interface{ BunDB() *bun.DB }:
		return v.BunDB()
	case interface{ Unwrap() *bun.DB }:
		return v.Unwrap()
	default:
		return nil
	}
}

// NewQuery 构造针对指定实体类型的泛型查询与仓储器。
func NewQuery[T any](db bun.IDB) *Query[T] {
	baseDB := UnwrapBunDB(db)
	var sel *bun.SelectQuery
	if db != nil {
		sel = db.NewSelect().Model((*T)(nil))
	}
	return &Query[T]{
		db:     db,
		baseDB: baseDB,
		sel:    sel,
	}
}

// NewQueryFrom 根据上下文自动感知活跃事务或回退至 fallback 数据库构造查询器。
func NewQueryFrom[T any](ctx context.Context, fallback bun.IDB) *Query[T] {
	resolved := ResolveDB(ctx, fallback)
	q := NewQuery[T](resolved)
	if q.baseDB == nil {
		q.baseDB = UnwrapBunDB(fallback)
	}
	return q
}

// RawQuery 返回底层的 *bun.SelectQuery，便于需要底层操作的高级场景。
func (q *Query[T]) RawQuery() *bun.SelectQuery {
	return q.sel
}

// Clone 返回当前 Query[T] 的深拷贝独立副本，杜绝多分支派生查询之间的状态交叉污染。
func (q *Query[T]) Clone() *Query[T] {
	return q.withSubSelect(q.sel.Clone())
}

// ToSQL 返回当前组装好的带占位符与参数绑定的标准 SQL 字符串表示，便于零副作用诊断。
func (q *Query[T]) ToSQL() string {
	return q.BuildSelect().String()
}

// Transaction 在当前数据库连接上启动事务并执行闭包，自动将活跃事务注入 Context 并处理提交与回滚。
func (q *Query[T]) Transaction(ctx context.Context, fn func(txCtx context.Context) error) error {
	if bdb := UnwrapBunDB(q.db); bdb != nil {
		return bdb.RunInTx(ctx, nil, func(c context.Context, tx bun.Tx) error {
			return fn(WithDB(c, tx))
		})
	}
	if q.db != nil {
		return fn(WithDB(ctx, q.db))
	}
	return fn(ctx)
}

// resolveDB 优先从 Context 提取活跃事务或连接，无活跃连接时回退至 q.db。
func (q *Query[T]) resolveDB(ctx context.Context) bun.IDB {
	return ResolveDB(ctx, q.db)
}

// defaultString 若首个参数非空则优先使用，否则返回默认值。
func defaultString(fallback string, values []string) string {
	return cmp.Or(lo.FirstOrEmpty(values), fallback)
}

// isModel 判断类型 T 是否实现了 Model 接口。
func isModel[T any]() bool {
	var zero T
	_, ok := any(&zero).(Model)
	return ok
}

// isSoftDeletable 判断类型 T 是否实现了 SoftDeletable 接口。
func isSoftDeletable[T any]() bool {
	var zero T
	_, ok := any(&zero).(SoftDeletable)
	return ok
}

// toIdents 将字符串切片转换为 bun.Ident 标识符切片。
func toIdents(columns []string) []any {
	return lo.Map(columns, func(col string, _ int) any {
		return bun.Ident(col)
	})
}
