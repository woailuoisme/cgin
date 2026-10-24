package factories

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/uptrace/bun"

	"bgin/internal/database"
)

// ErrDatabaseRequired 在缺少数据库连接且调用持久化方法时返回。
var ErrDatabaseRequired = errors.New("factory: database is required for Create or CreateMany")

// DefinitionFunc 定义单个实体的默认属性生成函数。
type DefinitionFunc[T any] func(f *gofakeit.Faker) *T

// StateFunc 定义对实体状态进行修改的函数。
type StateFunc[T any] func(item *T)

// SequenceFunc 定义根据索引动态计算字段的函数。
type SequenceFunc[T any] func(item *T, index int)

// HookFunc 定义生成后或持久化后的生命周期钩子函数。
type HookFunc[T any] func(ctx context.Context, item *T) error

// ProgressFunc 定义批量持久化每批写入完成后的进度通知函数。
type ProgressFunc func(current, total int)

// Factory 是泛型模型工厂基座，支持不可变链式调用、内存快速构建与分块事务持久化。
type Factory[T any] struct {
	db            bun.IDB
	faker         *gofakeit.Faker
	count         int
	chunkSize     int
	definition    DefinitionFunc[T]
	states        []StateFunc[T]
	sequences     []SequenceFunc[T]
	afterMaking   []HookFunc[T]
	afterCreating []HookFunc[T]
	onProgress    ProgressFunc
}

// New 构造一个新的泛型模型工厂基座。
func New[T any](db bun.IDB, def DefinitionFunc[T]) *Factory[T] {
	var seed uint64
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err == nil {
		seed = binary.LittleEndian.Uint64(b[:])
	}

	return &Factory[T]{
		db:            db,
		faker:         gofakeit.New(seed),
		count:         1,
		chunkSize:     1000,
		definition:    def,
		states:        make([]StateFunc[T], 0),
		sequences:     make([]SequenceFunc[T], 0),
		afterMaking:   make([]HookFunc[T], 0),
		afterCreating: make([]HookFunc[T], 0),
	}
}

// clone 产生当前工厂实例的浅拷贝副本，保持状态与切片的不可变隔离。
func (f *Factory[T]) clone() *Factory[T] {
	copied := *f
	copied.states = append([]StateFunc[T](nil), f.states...)
	copied.sequences = append([]SequenceFunc[T](nil), f.sequences...)
	copied.afterMaking = append([]HookFunc[T](nil), f.afterMaking...)
	copied.afterCreating = append([]HookFunc[T](nil), f.afterCreating...)
	return &copied
}

// Count 设置批量生成的数量，返回工厂副本。
func (f *Factory[T]) Count(count int) *Factory[T] {
	cp := f.clone()
	cp.count = max(count, 1)
	return cp
}

// ChunkSize 设置批量持久化时的分块大小，返回工厂副本。
func (f *Factory[T]) ChunkSize(size int) *Factory[T] {
	cp := f.clone()
	if size < 1 {
		size = 1000
	}
	cp.chunkSize = size
	return cp
}

// OnProgress 注册批量持久化分批写入时的进度回调钩子，返回工厂副本。
func (f *Factory[T]) OnProgress(fn ProgressFunc) *Factory[T] {
	cp := f.clone()
	cp.onProgress = fn
	return cp
}

// Seed 设置确定性随机数种子，返回工厂副本。
func (f *Factory[T]) Seed(seed uint64) *Factory[T] {
	cp := f.clone()
	cp.faker = gofakeit.New(seed)
	return cp
}

// State 附加状态转换函数，返回工厂副本。
func (f *Factory[T]) State(fn StateFunc[T]) *Factory[T] {
	if fn == nil {
		return f
	}
	cp := f.clone()
	cp.states = append(cp.states, fn)
	return cp
}

// Sequence 附加基于下标的序列计算函数，返回工厂副本。
func (f *Factory[T]) Sequence(fn SequenceFunc[T]) *Factory[T] {
	if fn == nil {
		return f
	}
	cp := f.clone()
	cp.sequences = append(cp.sequences, fn)
	return cp
}

// AfterMaking 注册实体在内存构建完成后的回调钩子，返回工厂副本。
func (f *Factory[T]) AfterMaking(fn HookFunc[T]) *Factory[T] {
	if fn == nil {
		return f
	}
	cp := f.clone()
	cp.afterMaking = append(cp.afterMaking, fn)
	return cp
}

// AfterCreating 注册实体在持久化成功后的回调钩子，返回工厂副本。
func (f *Factory[T]) AfterCreating(fn HookFunc[T]) *Factory[T] {
	if fn == nil {
		return f
	}
	cp := f.clone()
	cp.afterCreating = append(cp.afterCreating, fn)
	return cp
}

// DB 返回工厂关联的数据库连接。
func (f *Factory[T]) DB() bun.IDB {
	return f.db
}

// Faker 返回工厂当前的随机生成器。
func (f *Factory[T]) Faker() *gofakeit.Faker {
	return f.faker
}

// Make 在内存中生成单个实体（不写数据库），并依次应用状态与 AfterMaking 钩子。
func (f *Factory[T]) Make(ctx context.Context) (*T, error) {
	item := f.makeOne(0)
	for _, hook := range f.afterMaking {
		if err := hook(ctx, item); err != nil {
			return nil, fmt.Errorf("factory after-making hook failed: %w", err)
		}
	}
	return item, nil
}

// MakeMany 在内存中生成指定数量的实体切片。
func (f *Factory[T]) MakeMany(ctx context.Context, counts ...int) ([]*T, error) {
	total := f.count
	if len(counts) > 0 && counts[0] > 0 {
		total = counts[0]
	}

	items := make([]*T, 0, total)
	for i := range total {
		item := f.makeOne(i)
		for _, hook := range f.afterMaking {
			if err := hook(ctx, item); err != nil {
				return nil, fmt.Errorf("factory after-making hook failed at index %d: %w", i, err)
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// Create 在内存生成实体并持久化保存至数据库，同时触发所有钩子。
func (f *Factory[T]) Create(ctx context.Context) (*T, error) {
	if f.db == nil {
		return nil, ErrDatabaseRequired
	}

	item, err := f.Make(ctx)
	if err != nil {
		return nil, err
	}

	db := database.ResolveDB(ctx, f.db)
	if _, err := db.NewInsert().Model(item).Exec(ctx); err != nil {
		return nil, fmt.Errorf("factory create failed to insert model: %w", err)
	}

	for _, hook := range f.afterCreating {
		if err := hook(ctx, item); err != nil {
			return nil, fmt.Errorf("factory after-creating hook failed: %w", err)
		}
	}
	return item, nil
}

// CreateMany 批量创建并持久化保存实体切片。
// 会在单一事务中以 ChunkSize 为单位分批写入数据库，最大化写入吞吐。
func (f *Factory[T]) CreateMany(ctx context.Context, counts ...int) ([]*T, error) {
	if f.db == nil {
		return nil, ErrDatabaseRequired
	}

	total := f.count
	if len(counts) > 0 && counts[0] > 0 {
		total = counts[0]
	}

	items, err := f.MakeMany(ctx, total)
	if err != nil {
		return nil, err
	}

	if total == 0 {
		return items, nil
	}

	// 支持从 bun.DB 或已有事务中执行
	runInTx := func(tx bun.IDB) error {
		txCtx := database.WithDB(ctx, tx)
		chunkSize := f.chunkSize
		for i := 0; i < len(items); i += chunkSize {
			end := min(i+chunkSize, len(items))
			chunk := items[i:end]
			if _, err := tx.NewInsert().Model(&chunk).Exec(txCtx); err != nil {
				return fmt.Errorf("factory create-many insert chunk [%d:%d] failed: %w", i, end, err)
			}
			if f.onProgress != nil {
				f.onProgress(end, len(items))
			}
		}

		for idx, item := range items {
			for _, hook := range f.afterCreating {
				if err := hook(txCtx, item); err != nil {
					return fmt.Errorf("factory after-creating hook failed at index %d: %w", idx, err)
				}
			}
		}
		return nil
	}

	switch db := f.db.(type) {
	case *bun.DB:
		if err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			return runInTx(&tx)
		}); err != nil {
			return nil, err
		}
	default:
		// 已在外部事务中
		if err := runInTx(db); err != nil {
			return nil, err
		}
	}

	return items, nil
}

// makeOne 内部构建单个实体并应用 State 与 Sequence。
func (f *Factory[T]) makeOne(index int) *T {
	var item *T
	if f.definition != nil {
		item = f.definition(f.faker)
	} else {
		item = new(T)
	}

	for _, seq := range f.sequences {
		seq(item, index)
	}
	for _, state := range f.states {
		state(item)
	}
	return item
}
