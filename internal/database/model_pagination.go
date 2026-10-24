package database

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"time"

	"github.com/uptrace/bun"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

func normalizePagination(page, pageSize int) (int, int) {
	return max(page, 1), normalizeLimit(pageSize)
}

func normalizeLimit(limit int) int {
	if limit < 1 {
		return defaultPageSize
	}
	return min(limit, maxPageSize)
}

// Paginate 执行分页查询，返回分页结果切片与总条数。
// 遵循 api.md 契约：缺省页码为 1，缺省页大小为 20，单页最大上限截断为 100。
func (q *Query[T]) Paginate(ctx context.Context, page, pageSize int) ([]T, int, error) {
	page, pageSize = normalizePagination(page, pageSize)
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return make([]T, 0), 0, nil
	}
	items, err := q.Offset((page - 1) * pageSize).Limit(pageSize).Get(ctx)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// SimplePaginate 执行轻量简易分页（对标 Laravel simplePaginate）。
// 仅查询 pageSize + 1 条记录以判断是否有下一页，完全免除全表 COUNT(*) 统计开销，特别适合移动端或海量数据无限流加载场景。
func (q *Query[T]) SimplePaginate(ctx context.Context, page, pageSize int) ([]T, bool, error) {
	page, pageSize = normalizePagination(page, pageSize)

	cloned := q.Clone()
	items, err := cloned.Offset((page - 1) * pageSize).Limit(pageSize + 1).Get(ctx)
	if err != nil {
		return nil, false, err
	}

	hasMore := len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	if items == nil {
		items = make([]T, 0)
	}

	return items, hasMore, nil
}

// CursorPayload 封装基于 (created_at, id) 的游标数据载荷。
type CursorPayload struct {
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"id"`
}

// EncodeCursor 将创建时间与主键 ID 编码为 URL 安全的 Base64 游标字符串。
func EncodeCursor(createdAt time.Time, id string) string {
	if createdAt.IsZero() || id == "" {
		return ""
	}
	payload := CursorPayload{
		CreatedAt: createdAt.UTC(),
		ID:        id,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodeCursor 将 URL 安全的 Base64 游标解析为对应的时间戳与主键 ID。若游标无效则返回 false。
func DecodeCursor(cursor string) (time.Time, string, bool) {
	if cursor == "" {
		return time.Time{}, "", false
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", false
	}
	var payload CursorPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return time.Time{}, "", false
	}
	if payload.ID == "" || payload.CreatedAt.IsZero() {
		return time.Time{}, "", false
	}
	return payload.CreatedAt.UTC(), payload.ID, true
}

func extractCursorInfo(item any) (time.Time, string, bool) {
	if item == nil {
		return time.Time{}, "", false
	}
	if ts, ok := item.(Timestamped); ok {
		return ts.GetCreatedAt(), ts.GetID(), true
	}
	return extractCursorViaReflection(item)
}

func extractCursorViaReflection(item any) (time.Time, string, bool) {
	val := reflect.ValueOf(item)
	if val.Kind() == reflect.Pointer {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return time.Time{}, "", false
	}

	idField := val.FieldByName("ID")
	createdAtField := val.FieldByName("CreatedAt")
	if !idField.IsValid() || !createdAtField.IsValid() {
		return time.Time{}, "", false
	}

	idStr, isIDString := reflect.TypeAssert[string](idField)
	createdAt, isTime := reflect.TypeAssert[time.Time](createdAtField)
	if isIDString && isTime {
		return createdAt, idStr, true
	}
	return time.Time{}, "", false
}

// CursorPaginate 执行基于 (created_at, id) 复合游标的高性能 seek 分页（对标 Laravel cursorPaginate）。
// 内部使用 PostgreSQL 元组比较 ("created_at", "id") < (?, ?)，执行时间恒定为 O(1)，绝不产生深分页性能悬崖。
// 默认按创建时间倒序排（最新在前）。若有下一页，返回非空 nextCursor 与 hasMore = true。
func (q *Query[T]) CursorPaginate(ctx context.Context, cursor string, limit int) (items []T, nextCursor string, hasMore bool, err error) {
	limit = normalizeLimit(limit)

	cloned := q.Clone()
	if t, id, ok := DecodeCursor(cursor); ok {
		cloned = cloned.WhereRow([]string{"created_at", "id"}, "<", t, id)
	}

	cloned = cloned.OrderExpr("?TableAlias.? DESC, ?TableAlias.? DESC", bun.Ident("created_at"), bun.Ident("id")).Limit(limit + 1)

	items, err = cloned.Get(ctx)
	if err != nil {
		return nil, "", false, err
	}

	hasMore = len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	if items == nil {
		items = make([]T, 0)
	}

	if hasMore && len(items) > 0 {
		if createdAt, id, ok := extractCursorInfo(&items[len(items)-1]); ok {
			nextCursor = EncodeCursor(createdAt, id)
		}
	}

	return items, nextCursor, hasMore, nil
}

// resolvePaginationChunkSize 确保分块大小为正数：优先使用传入值，其次使用 Query.chunkSize，缺省为 100。
func (q *Query[T]) resolvePaginationChunkSize(size int) int {
	if size > 0 {
		return size
	}
	return cmp.Or(q.chunkSize, 100)
}

// Chunk 以固定大小的游标批量流式加载数据并执行回调闭包，彻底规避全量数据加载导致内存溢出。
// 内部使用克隆查询，确保不会副作用修改原 Query 状态。
func (q *Query[T]) Chunk(ctx context.Context, chunkSize int, fn func(items []T) error) error {
	chunkSize = q.resolvePaginationChunkSize(chunkSize)
	page := 1
	for {
		cloned := q.Clone()
		items, err := cloned.Offset((page - 1) * chunkSize).Limit(chunkSize).Get(ctx)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			break
		}
		if err := fn(items); err != nil {
			return err
		}
		if len(items) < chunkSize {
			break
		}
		page++
	}
	return nil
}

// ChunkByID 基于主键 ID 顺序（id > lastID）执行分块处理，对标 Laravel chunkById。
// 在迭代处理中执行修改或删除记录时，必须使用 ChunkByID 替代基于 OFFSET 的 Chunk，以避免漏查或数据重复。
func (q *Query[T]) ChunkByID(ctx context.Context, chunkSize int, fn func(items []T) error) error {
	chunkSize = q.resolvePaginationChunkSize(chunkSize)
	lastID := ""
	for {
		cloned := q.Clone()
		if lastID != "" {
			cloned = cloned.Where("?TableAlias.id > ?", lastID)
		}
		items, err := cloned.Order("?TableAlias.id ASC").Limit(chunkSize).Get(ctx)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			break
		}
		if err := fn(items); err != nil {
			return err
		}
		if len(items) < chunkSize {
			break
		}
		lastItem := items[len(items)-1]
		if m, ok := any(&lastItem).(Model); ok {
			lastID = m.GetID()
		} else {
			break
		}
	}
	return nil
}

var errStopChunk = errors.New("database: stop chunk iteration")

// ChunkWhile 以固定大小分块加载数据并执行回调，当回调返回 false 时立即提前终止处理（对标 Laravel chunkWhile）。
func (q *Query[T]) ChunkWhile(ctx context.Context, chunkSize int, fn func(items []T) bool) error {
	err := q.Chunk(ctx, chunkSize, func(items []T) error {
		if !fn(items) {
			return errStopChunk
		}
		return nil
	})
	if errors.Is(err, errStopChunk) {
		return nil
	}
	return err
}

// ChunkByIDWhile 基于主键 ID 顺序分块处理，当回调返回 false 时立即提前终止分块循环。
func (q *Query[T]) ChunkByIDWhile(ctx context.Context, chunkSize int, fn func(items []T) bool) error {
	err := q.ChunkByID(ctx, chunkSize, func(items []T) error {
		if !fn(items) {
			return errStopChunk
		}
		return nil
	})
	if errors.Is(err, errStopChunk) {
		return nil
	}
	return err
}

// LazyByID 基于主键索引（id > lastID）分批流式加载并逐条迭代，对标 Laravel 13 lazyById()。
// 与普通 Lazy 相比，LazyByID 针对海量数据不会长久占用数据库游标与连接，彻底避免 OFFSET 性能断崖。
func (q *Query[T]) LazyByID(ctx context.Context, chunkSize int) iter.Seq2[*T, error] {
	chunkSize = q.resolvePaginationChunkSize(chunkSize)
	return func(yield func(*T, error) bool) {
		lastID := ""
		for {
			cloned := q.Clone()
			if lastID != "" {
				cloned = cloned.Where("?TableAlias.id > ?", lastID)
			}
			items, err := cloned.Order("?TableAlias.id ASC").Limit(chunkSize).Get(ctx)
			if err != nil {
				yield(nil, err)
				return
			}
			if len(items) == 0 {
				return
			}
			for i := range items {
				item := &items[i]
				if m, ok := any(item).(Model); ok {
					lastID = m.GetID()
				}
				if !yield(item, nil) {
					return
				}
			}
			if len(items) < chunkSize {
				return
			}
		}
	}
}

// Lazy 使用 Go 1.23+ 原生迭代器 iter.Seq2 进行常量级内存流式遍历（对标 Laravel 13 lazy()）。
// 支持现代 Go range 语法：for item, err := range query.Lazy(ctx) { ... }
func (q *Query[T]) Lazy(ctx context.Context) iter.Seq2[*T, error] {
	return func(yield func(*T, error) bool) {
		scanDB := q.baseDB
		if scanDB == nil {
			yield(nil, errors.New("database: cursor streaming requires *bun.DB instance"))
			return
		}

		sel := q.BuildSelect()
		rows, err := sel.Rows(ctx)
		if err != nil {
			yield(nil, err)
			return
		}
		defer func() {
			_ = rows.Close()
		}()

		for rows.Next() {
			var item T
			if err := scanDB.ScanRow(ctx, rows, &item); err != nil {
				yield(nil, err)
				return
			}
			if dt, ok := any(&item).(DirtyTrackable); ok {
				dt.SyncOriginal()
			}
			if !yield(&item, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(nil, err)
		}
	}
}

// Cursor 使用数据库底层原生 Rows 游标以恒定 O(1) 内存流式迭代匹配记录（对标 Laravel cursor）。
// 与基于 Offset 的 Chunk 相比，Cursor 彻底规避了深分页性能断崖与全量数据载入的 OOM 风险。
func (q *Query[T]) Cursor(ctx context.Context, fn func(item *T) error) error {
	for item, err := range q.Lazy(ctx) {
		if err != nil {
			return err
		}
		if err := fn(item); err != nil {
			return err
		}
	}
	return nil
}
