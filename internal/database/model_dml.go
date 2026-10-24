package database

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/samber/lo"
	"github.com/uptrace/bun"
)

// targetDB 优先提取显式传入的数据库/事务连接，缺省时回退至 q.db。
func (q *Query[T]) targetDB(dbs ...bun.IDB) bun.IDB {
	if len(dbs) > 0 && dbs[0] != nil {
		return dbs[0]
	}
	return q.db
}

// buildCounter 构建原子计数器增减更新查询。
func (q *Query[T]) buildCounter(column, op string, delta int64, dbs ...bun.IDB) *bun.UpdateQuery {
	upd := q.targetDB(dbs...).NewUpdate().
		Model((*T)(nil)).
		Set("? = ? "+op+" ?", bun.Ident(column), bun.Ident(column), delta)

	if isModel[T]() {
		upd = upd.Set("updated_at = ?", time.Now().UTC())
	}

	return upd.Where("?TableAlias.id IN (?)", q.BuildSelect().Column("id"))
}

// BuildIncrement 构建原子计数器递增更新查询（对标 Laravel increment）。
func (q *Query[T]) BuildIncrement(column string, delta int64) *bun.UpdateQuery {
	return q.buildCounter(column, "+", delta)
}

// Increment 对匹配当前条件的记录执行原子计数器递增（缺省步长为 1，自动透传上下文活跃事务）。
func (q *Query[T]) Increment(ctx context.Context, column string, amount ...int64) (sql.Result, error) {
	return q.buildCounter(column, "+", extractDelta(amount), q.resolveDB(ctx)).Exec(ctx)
}

// BuildDecrement 构建原子计数器递减更新查询（对标 Laravel decrement）。
func (q *Query[T]) BuildDecrement(column string, delta int64) *bun.UpdateQuery {
	return q.buildCounter(column, "-", delta)
}

// Decrement 对匹配当前条件的记录执行原子计数器递减（缺省步长为 1，自动透传上下文活跃事务）。
func (q *Query[T]) Decrement(ctx context.Context, column string, amount ...int64) (sql.Result, error) {
	return q.buildCounter(column, "-", extractDelta(amount), q.resolveDB(ctx)).Exec(ctx)
}

func extractDelta(amount []int64) int64 {
	return cmp.Or(lo.FirstOrEmpty(amount), 1)
}

// BuildTruncate 构建清空当前模型对应表的 DDL 查询。
func (q *Query[T]) BuildTruncate(dbs ...bun.IDB) *bun.TruncateTableQuery {
	return q.targetDB(dbs...).NewTruncateTable().Model((*T)(nil))
}

// Truncate 快速清空当前模型对应的数据库表（对标 Laravel truncate，自动透传上下文活跃事务）。
func (q *Query[T]) Truncate(ctx context.Context) error {
	_, err := q.BuildTruncate(q.resolveDB(ctx)).Exec(ctx)
	return err
}

// BuildInsert 构建当前模型的批量插入查询。
func (q *Query[T]) BuildInsert(items []*T, dbs ...bun.IDB) *bun.InsertQuery {
	return q.targetDB(dbs...).NewInsert().Model(&items)
}

// BuildUpsert 构建当前模型的冲突更新查询（ON CONFLICT ... DO UPDATE）。
func (q *Query[T]) BuildUpsert(items []*T, conflictColumns []string, updateColumns ...string) *bun.InsertQuery {
	ins := q.BuildInsert(items).On("CONFLICT (?) DO UPDATE", bun.List(toIdents(conflictColumns)))
	for _, col := range updateColumns {
		ins = ins.Set("? = EXCLUDED.?", bun.Ident(col), bun.Ident(col))
	}
	return ins
}

// BuildInsertOrIgnore 构建当前模型的忽略冲突插入查询（ON CONFLICT DO NOTHING）。
func (q *Query[T]) BuildInsertOrIgnore(items []*T) *bun.InsertQuery {
	return q.BuildInsert(items).Ignore()
}

// ChunkSize 设置当前查询与批量持久化操作的分块批次大小（如在超宽表或超大批量场景下覆盖默认的 DefaultChunkSize）。
func (q *Query[T]) ChunkSize(size int) *Query[T] {
	if size > 0 {
		q.chunkSize = size
	}
	return q
}

// effectiveChunkSize 返回当前查询器生效的分块批次大小。
func (q *Query[T]) effectiveChunkSize() int {
	return cmp.Or(q.chunkSize, DefaultChunkSize)
}

// Create 仓储算子：将新实体持久化至数据库（自动透传事务连接）。
func (q *Query[T]) Create(ctx context.Context, item *T) error {
	return Insert(ctx, q.resolveDB(ctx), item)
}

// CreateMany 仓储算子：批量持久化一组新实体，底层通过单条多行 VALUES SQL 执行，性能远超逐条单测。
// 当待插入数量超出当前生效的 ChunkSize 时，自动委托给 InsertManyChunked 进行分块安全持久化。
func (q *Query[T]) CreateMany(ctx context.Context, items []*T) error {
	chunkSize := q.effectiveChunkSize()
	if len(items) > chunkSize {
		return InsertManyChunked(ctx, q.resolveDB(ctx), items, chunkSize)
	}
	return InsertMany(ctx, q.resolveDB(ctx), items)
}

// Upsert 仓储算子：批量原子插入或冲突时更新指定字段（对标 Laravel upsert）。
// 当待更新数量超出当前生效的 ChunkSize 时，自动委托给 UpsertChunked 进行分块安全持久化。
func (q *Query[T]) Upsert(ctx context.Context, items []*T, conflictColumns []string, updateColumns ...string) error {
	chunkSize := q.effectiveChunkSize()
	if len(items) > chunkSize {
		return UpsertChunked(ctx, q.resolveDB(ctx), items, chunkSize, conflictColumns, updateColumns...)
	}
	return Upsert(ctx, q.resolveDB(ctx), items, conflictColumns, updateColumns...)
}

// InsertOrIgnore 仓储算子：批量插入实体，若存在主键或唯一约束冲突则静默忽略不报错（对标 Laravel insertOrIgnore）。
// 当待插入数量超出当前生效的 ChunkSize 时，自动委托给 InsertOrIgnoreChunked 进行分块安全持久化。
func (q *Query[T]) InsertOrIgnore(ctx context.Context, items []*T) error {
	chunkSize := q.effectiveChunkSize()
	if len(items) > chunkSize {
		return InsertOrIgnoreChunked(ctx, q.resolveDB(ctx), items, chunkSize)
	}
	return InsertOrIgnore(ctx, q.resolveDB(ctx), items)
}

// InsertOrIgnoreChunked 仓储算子：分块批量忽略冲突插入。
func (q *Query[T]) InsertOrIgnoreChunked(ctx context.Context, items []*T, chunkSize int) error {
	if chunkSize <= 0 {
		chunkSize = q.effectiveChunkSize()
	}
	return InsertOrIgnoreChunked(ctx, q.resolveDB(ctx), items, chunkSize)
}

// Save 仓储算子：根据主键智能插入或更新实体。
func (q *Query[T]) Save(ctx context.Context, item *T) error {
	if m, ok := any(item).(Model); ok {
		return Save(ctx, q.resolveDB(ctx), m)
	}
	return errors.New("database: item must implement Model interface to save")
}

// Update 仓储算子：更新实体指定列或全量更新。
func (q *Query[T]) Update(ctx context.Context, item *T, columns ...string) error {
	return Update(ctx, q.resolveDB(ctx), item, columns...)
}

// Delete 仓储算子：删除实体（支持软删除模型自动降级为软删除）。
func (q *Query[T]) Delete(ctx context.Context, item *T) error {
	db := q.resolveDB(ctx)
	if m, ok := any(item).(Model); ok {
		return Delete(ctx, db, m)
	}
	return ForceDelete(ctx, db, item)
}

// DeleteByID 仓储算子：按主键查找并删除对应实体。
func (q *Query[T]) DeleteByID(ctx context.Context, id string) error {
	item, err := q.Find(ctx, id)
	if err != nil {
		return err
	}
	return q.Delete(ctx, item)
}

// FirstOrCreate 查找满足当前查询条件的首条记录；未命中则使用 item 自动持久化新记录并返回 (record, true, nil)。
func (q *Query[T]) FirstOrCreate(ctx context.Context, item *T) (*T, bool, error) {
	existing, err := q.First(ctx)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	if err := q.Create(ctx, item); err != nil {
		return nil, false, err
	}
	return item, true, nil
}

// UpdateOrCreate 查找满足当前查询条件的首条记录；命中则更新 updateColumns 指定列并返回 (existing, false, nil)；未命中则持久化新记录返回 (item, true, nil)。
func (q *Query[T]) UpdateOrCreate(ctx context.Context, item *T, updateColumns ...string) (*T, bool, error) {
	existing, err := q.First(ctx)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
		if err := q.Create(ctx, item); err != nil {
			return nil, false, err
		}
		return item, true, nil
	}

	mExisting, isExistingModel := any(existing).(Model)
	mItem, isItemModel := any(item).(Model)
	if isExistingModel && isItemModel {
		mItem.SetID(mExisting.GetID())
	}

	if err := q.Update(ctx, item, updateColumns...); err != nil {
		return nil, false, err
	}
	return item, false, nil
}

// Touch 仓储算子：刷新实体 updated_at 时间戳并落库。
func (q *Query[T]) Touch(ctx context.Context, item *T) error {
	if m, ok := any(item).(Model); ok {
		return Touch(ctx, q.resolveDB(ctx), m)
	}
	return errors.New("database: item must implement Model interface to touch")
}

// Restore 仓储算子：恢复已软删除的实体（对标 Laravel $model->restore()）。
func (q *Query[T]) Restore(ctx context.Context, item *T) error {
	if sd, ok := any(item).(SoftDeletable); ok {
		return Restore(ctx, q.resolveDB(ctx), sd)
	}
	return errors.New("database: item must implement SoftDeletable to restore")
}

// RestoreAll 批量恢复当前查询匹配的所有软删除记录（对标 Laravel withTrashed()->restore()，自动透传上下文活跃事务）。
func (q *Query[T]) RestoreAll(ctx context.Context) (sql.Result, error) {
	if !isSoftDeletable[T]() {
		return nil, errors.New("database: model does not support soft deletes")
	}
	upd := q.resolveDB(ctx).NewUpdate().
		Model((*T)(nil)).
		Set("deleted_at = NULL").
		Set("updated_at = ?", time.Now().UTC()).
		Where("?TableAlias.id IN (?)", q.OnlyTrashed().BuildSelect().Column("id"))
	return upd.Exec(ctx)
}

// UpdateMany 仓储算子：批量更新多行实体的指定列（基于 Bun 原生 Bulk() 高性能执行）。
// 当待更新数量超出当前生效的 ChunkSize 时，自动委托给 UpdateManyChunked 进行分块安全持久化。
func (q *Query[T]) UpdateMany(ctx context.Context, items []*T, columns ...string) error {
	chunkSize := q.effectiveChunkSize()
	if len(items) > chunkSize {
		return UpdateManyChunked(ctx, q.resolveDB(ctx), items, chunkSize, columns...)
	}
	return UpdateMany(ctx, q.resolveDB(ctx), items, columns...)
}

// InsertManyChunked 仓储算子：分块批量插入大量实体，严格规避单条 SQL 绑参数量超过 PostgreSQL 65535 上限。
func (q *Query[T]) InsertManyChunked(ctx context.Context, items []*T, chunkSize int) error {
	if chunkSize <= 0 {
		chunkSize = q.effectiveChunkSize()
	}
	return InsertManyChunked(ctx, q.resolveDB(ctx), items, chunkSize)
}

// UpsertChunked 仓储算子：分块批量原子 Upsert，极少 SQL 数量完成海量数据幂等同步。
func (q *Query[T]) UpsertChunked(ctx context.Context, items []*T, chunkSize int, conflictColumns []string, updateColumns ...string) error {
	if chunkSize <= 0 {
		chunkSize = q.effectiveChunkSize()
	}
	return UpsertChunked(ctx, q.resolveDB(ctx), items, chunkSize, conflictColumns, updateColumns...)
}

// UpdateManyChunked 仓储算子：分块批量更新多行实体。
func (q *Query[T]) UpdateManyChunked(ctx context.Context, items []*T, chunkSize int, columns ...string) error {
	if chunkSize <= 0 {
		chunkSize = q.effectiveChunkSize()
	}
	return UpdateManyChunked(ctx, q.resolveDB(ctx), items, chunkSize, columns...)
}

// UpdateMatching 对满足当前查询条件的所有记录执行批量字段更新，返回被影响的行数（对标 Laravel Model::where(...)->update(...)，自动透传上下文活跃事务）。
func (q *Query[T]) UpdateMatching(ctx context.Context, setValues map[string]any) (sql.Result, error) {
	if len(setValues) == 0 {
		return nil, errors.New("database: setValues cannot be empty")
	}
	upd := q.resolveDB(ctx).NewUpdate().Model((*T)(nil))
	for col, val := range setValues {
		upd = upd.Set("? = ?", bun.Ident(col), val)
	}
	if isModel[T]() {
		upd = upd.Set("updated_at = ?", time.Now().UTC())
	}
	return upd.Where("?TableAlias.id IN (?)", q.BuildSelect().Column("id")).Exec(ctx)
}

// DeleteMatching 对满足当前查询条件的所有记录执行批量删除（自动遵循软删除规则与上下文活跃事务，对标 Laravel Model::where(...)->delete()）。
func (q *Query[T]) DeleteMatching(ctx context.Context) (sql.Result, error) {
	targetDB := q.resolveDB(ctx)
	if isSoftDeletable[T]() && !q.withTrashed {
		return targetDB.NewUpdate().
			Model((*T)(nil)).
			Set("deleted_at = ?", time.Now().UTC()).
			Set("updated_at = ?", time.Now().UTC()).
			Where("?TableAlias.id IN (?)", q.BuildSelect().Column("id")).
			Exec(ctx)
	}
	return targetDB.NewDelete().
		Model((*T)(nil)).
		Where("?TableAlias.id IN (?)", q.BuildSelect().Column("id")).
		Exec(ctx)
}

// ============================================================================
// 通用持久化算子 (Top-Level Persistence Functions)
// ============================================================================

// Touch 仅将实体的 updated_at 刷新为当前 UTC 时间并落库（对标 Laravel $model->touch()）。
func Touch[T Model](ctx context.Context, db bun.IDB, m T) error {
	actualDB := ResolveDB(ctx, db)
	now := time.Now().UTC()
	_, err := actualDB.NewUpdate().
		Model(m).
		Set("updated_at = ?", now).
		WherePK().
		Exec(ctx)
	return err
}

// Save 根据主键是否为空，自动决策执行插入或增量更新（自动感知 Context 活跃事务与脏检查）。
func Save[T Model](ctx context.Context, db bun.IDB, m T) error {
	actualDB := ResolveDB(ctx, db)
	if m.GetID() == "" {
		if err := Insert(ctx, actualDB, m); err != nil {
			return err
		}
		if dt, ok := any(m).(DirtyTrackable); ok {
			dt.SyncOriginal()
		}
		return nil
	}

	if dt, ok := any(m).(DirtyTrackable); ok {
		dirty := dt.GetDirty()
		if len(dirty) == 0 {
			// 未检测到任何脏字段，直接短路避免无谓数据库 I/O
			return nil
		}
		if !slices.Contains(dirty, "updated_at") {
			dirty = append(dirty, "updated_at")
		}
		if err := Update(ctx, actualDB, m, dirty...); err != nil {
			return err
		}
		dt.SyncOriginal()
		return nil
	}

	return Update(ctx, actualDB, m)
}

// runBeforeCreate 批量触发实体的 BeforeCreate 钩子。
func runBeforeCreate[T any](ctx context.Context, items []*T) error {
	for _, item := range items {
		if hook, ok := any(item).(BeforeCreator); ok {
			if err := hook.BeforeCreate(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// runAfterCreate 批量触发实体的 AfterCreate 钩子并同步变更追踪基准。
func runAfterCreate[T any](ctx context.Context, items []*T) error {
	for _, item := range items {
		if hook, ok := any(item).(AfterCreator); ok {
			if err := hook.AfterCreate(ctx); err != nil {
				return err
			}
		}
		if dt, ok := any(item).(DirtyTrackable); ok {
			dt.SyncOriginal()
		}
	}
	return nil
}

// DefaultChunkSize 是批量持久化操作单批次建议最大记录数，严格规避 PostgreSQL 单查询 65535 绑参限制。
const DefaultChunkSize = 500

// insertManyRaw 执行底层单条多行批量插入 SQL。
func insertManyRaw[T any](ctx context.Context, db bun.IDB, items []*T) error {
	if len(items) == 0 {
		return nil
	}
	if err := runBeforeCreate(ctx, items); err != nil {
		return err
	}
	q := NewQuery[T](ResolveDB(ctx, db))
	if _, err := q.BuildInsert(items).Exec(ctx); err != nil {
		return err
	}
	return runAfterCreate(ctx, items)
}

// InsertMany 执行单条多行高效批量插入（自动串联生命周期钩子并同步快照）。
// 当待插入数量超出 DefaultChunkSize 时，自动委托给 InsertManyChunked 进行分块安全持久化，防止参数超限。
func InsertMany[T any](ctx context.Context, db bun.IDB, items []*T) error {
	if len(items) > DefaultChunkSize {
		return InsertManyChunked(ctx, db, items, DefaultChunkSize)
	}
	return insertManyRaw(ctx, db, items)
}

// upsertRaw 执行底层单条批量原子 Upsert SQL。
func upsertRaw[T any](ctx context.Context, db bun.IDB, items []*T, conflictColumns []string, updateColumns ...string) error {
	if len(items) == 0 {
		return nil
	}
	if len(conflictColumns) == 0 {
		return errors.New("database: upsert requires at least one conflict column")
	}
	if err := runBeforeCreate(ctx, items); err != nil {
		return err
	}
	q := NewQuery[T](ResolveDB(ctx, db))
	if _, err := q.BuildUpsert(items, conflictColumns, updateColumns...).Exec(ctx); err != nil {
		return err
	}
	return runAfterCreate(ctx, items)
}

// Upsert 执行批量原子插入或冲突更新（对标 Laravel upsert）。
// conflictColumns 指定唯一性约束冲突列；updateColumns 指定发生冲突时需要更新的列集合。
// 当待更新数量超出 DefaultChunkSize 时，自动委托给 UpsertChunked 进行分块安全持久化。
func Upsert[T any](ctx context.Context, db bun.IDB, items []*T, conflictColumns []string, updateColumns ...string) error {
	if len(items) > DefaultChunkSize {
		return UpsertChunked(ctx, db, items, DefaultChunkSize, conflictColumns, updateColumns...)
	}
	return upsertRaw(ctx, db, items, conflictColumns, updateColumns...)
}

// insertOrIgnoreRaw 执行底层单条批量忽略冲突插入 SQL。
func insertOrIgnoreRaw[T any](ctx context.Context, db bun.IDB, items []*T) error {
	if len(items) == 0 {
		return nil
	}
	if err := runBeforeCreate(ctx, items); err != nil {
		return err
	}
	q := NewQuery[T](ResolveDB(ctx, db))
	if _, err := q.BuildInsertOrIgnore(items).Exec(ctx); err != nil {
		return err
	}
	return runAfterCreate(ctx, items)
}

// InsertOrIgnore 执行批量插入；若发生主键或唯一键冲突则静默忽略不报错（对标 Laravel insertOrIgnore）。
// 当待插入数量超出 DefaultChunkSize 时，自动分块安全持久化。
func InsertOrIgnore[T any](ctx context.Context, db bun.IDB, items []*T) error {
	if len(items) > DefaultChunkSize {
		return InsertOrIgnoreChunked(ctx, db, items, DefaultChunkSize)
	}
	return insertOrIgnoreRaw(ctx, db, items)
}

// Insert 将实体插入数据库（自动感知 Context 活跃事务并触发生命周期钩子）。
func Insert[T any](ctx context.Context, db bun.IDB, m T) error {
	if hook, ok := any(m).(BeforeCreator); ok {
		if err := hook.BeforeCreate(ctx); err != nil {
			return err
		}
	}
	actualDB := ResolveDB(ctx, db)
	if _, err := actualDB.NewInsert().Model(m).Exec(ctx); err != nil {
		return err
	}
	if hook, ok := any(m).(AfterCreator); ok {
		if err := hook.AfterCreate(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Update 显式更新实体指定列或全量更新（自动感知 Context 活跃事务并触发生命周期钩子）。
func Update[T any](ctx context.Context, db bun.IDB, m T, columns ...string) error {
	if hook, ok := any(m).(BeforeUpdater); ok {
		if err := hook.BeforeUpdate(ctx); err != nil {
			return err
		}
	}
	actualDB := ResolveDB(ctx, db)
	query := actualDB.NewUpdate().Model(m).WherePK()
	if len(columns) > 0 {
		query = query.Column(columns...)
	}
	if _, err := query.Exec(ctx); err != nil {
		return err
	}
	if hook, ok := any(m).(AfterUpdater); ok {
		if err := hook.AfterUpdate(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Delete 智能删除实体：若实体实现了 SoftDeletable 则执行软删除，否则执行物理硬删除（触发生命周期钩子）。
func Delete[T Model](ctx context.Context, db bun.IDB, m T) error {
	if hook, ok := any(m).(BeforeDeleter); ok {
		if err := hook.BeforeDelete(ctx); err != nil {
			return err
		}
	}
	actualDB := ResolveDB(ctx, db)
	var err error
	if sd, ok := any(m).(SoftDeletable); ok {
		err = SoftDelete(ctx, actualDB, sd)
	} else {
		err = ForceDelete(ctx, actualDB, m)
	}
	if err != nil {
		return err
	}
	if hook, ok := any(m).(AfterDeleter); ok {
		if err := hook.AfterDelete(ctx); err != nil {
			return err
		}
	}
	return nil
}

// SoftDelete 执行物理安全软删除（更新 deleted_at 和 updated_at）。
func SoftDelete[T SoftDeletable](ctx context.Context, db bun.IDB, m T) error {
	if m.GetID() == "" {
		return errors.New("database: cannot soft delete model with empty id")
	}
	actualDB := ResolveDB(ctx, db)
	now := time.Now().UTC()
	result, err := actualDB.NewUpdate().
		Model(m).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		WherePK().
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	m.SetDeletedAt(&now)
	return nil
}

// ForceDelete 强制从数据库中物理硬删除实体。
func ForceDelete[T any](ctx context.Context, db bun.IDB, m T) error {
	actualDB := ResolveDB(ctx, db)
	_, err := actualDB.NewDelete().Model(m).WherePK().Exec(ctx)
	return err
}

// Refresh 从数据库重新加载最新数据覆盖当前实体。
func Refresh[T any](ctx context.Context, db bun.IDB, m T) error {
	actualDB := ResolveDB(ctx, db)
	return actualDB.NewSelect().Model(m).WherePK().Scan(ctx)
}

// Restore 恢复已软删除的模型实体（将 deleted_at 置为 NULL，对标 Laravel $model->restore()）。
func Restore[T SoftDeletable](ctx context.Context, db bun.IDB, m T) error {
	if m.GetID() == "" {
		return errors.New("database: cannot restore model with empty id")
	}
	actualDB := ResolveDB(ctx, db)
	now := time.Now().UTC()
	result, err := actualDB.NewUpdate().
		Model(m).
		Set("deleted_at = NULL").
		Set("updated_at = ?", now).
		WherePK().
		Where("deleted_at IS NOT NULL").
		Exec(ctx)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	m.SetDeletedAt(nil)
	return nil
}

// updateManyRaw 执行底层单条批量更新 SQL。
func updateManyRaw[T any](ctx context.Context, db bun.IDB, items []*T, columns ...string) error {
	if len(items) == 0 {
		return nil
	}
	actualDB := ResolveDB(ctx, db)
	upd := actualDB.NewUpdate().Model(&items)
	if len(columns) > 0 {
		upd = upd.Column(columns...)
	}
	_, err := upd.Bulk().Exec(ctx)
	return err
}

// UpdateMany 基于 Bun 原生 Bulk() 特性单条 SQL 批量更新多行实体，性能远超循环执行单条 Update。
// 当待更新数量超出 DefaultChunkSize 时，自动委托给 UpdateManyChunked 进行分块安全持久化。
func UpdateMany[T any](ctx context.Context, db bun.IDB, items []*T, columns ...string) error {
	if len(items) > DefaultChunkSize {
		return UpdateManyChunked(ctx, db, items, DefaultChunkSize, columns...)
	}
	return updateManyRaw(ctx, db, items, columns...)
}

// chunkIter 针对切片执行通用分块遍历。
func chunkIter(total, size int, fn func(start, end int) error) error {
	if total == 0 {
		return nil
	}
	if size <= 0 {
		size = DefaultChunkSize
	}
	for i := 0; i < total; i += size {
		end := min(i+size, total)
		if err := fn(i, end); err != nil {
			return err
		}
	}
	return nil
}

// InsertManyChunked 自动分块批量插入大量实体，严格避免单条 SQL 绑参过多超过数据库上限（单次极少 SQL 数量）。
func InsertManyChunked[T any](ctx context.Context, db bun.IDB, items []*T, chunkSize int) error {
	return chunkIter(len(items), chunkSize, func(start, end int) error {
		return insertManyRaw(ctx, db, items[start:end])
	})
}

// UpsertChunked 自动分块批量原子 Upsert，防止超大批量操作超出数据库参数上限。
func UpsertChunked[T any](ctx context.Context, db bun.IDB, items []*T, chunkSize int, conflictColumns []string, updateColumns ...string) error {
	return chunkIter(len(items), chunkSize, func(start, end int) error {
		return upsertRaw(ctx, db, items[start:end], conflictColumns, updateColumns...)
	})
}

// InsertOrIgnoreChunked 自动分块批量忽略冲突插入。
func InsertOrIgnoreChunked[T any](ctx context.Context, db bun.IDB, items []*T, chunkSize int) error {
	return chunkIter(len(items), chunkSize, func(start, end int) error {
		return insertOrIgnoreRaw(ctx, db, items[start:end])
	})
}

// UpdateManyChunked 自动分块批量更新实体指定列。
func UpdateManyChunked[T any](ctx context.Context, db bun.IDB, items []*T, chunkSize int, columns ...string) error {
	return chunkIter(len(items), chunkSize, func(start, end int) error {
		return updateManyRaw(ctx, db, items[start:end], columns...)
	})
}
