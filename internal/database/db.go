package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cgin/internal/database/sqlc"
)

// ErrNotInitialised 表示数据访问入口未被装配（连接池缺失）。
var ErrNotInitialised = errors.New("database: connection pool is not initialised")

// DB 是数据访问入口：持有连接池与根查询集，并提供事务边界。
//
// 约定：读路径直接用 DB.Queries(ctx) 取生成查询集；需要原子性的写路径用
// DB.RunInTx，由事务闭包拿到与事务绑定的 *sqlc.Queries。嵌套调用通过 ctx 复用
// 同一事务，不需要逐层透传 *pgx.Tx。
type DB struct {
	pool *pgxpool.Pool
	root *sqlc.Queries
}

// NewDB 基于连接池构造数据访问入口；pool 为 nil 时返回 nil DB，
// 后续 Queries/RunInTx 会返回 ErrNotInitialised 或 nil，装配方必须传入有效连接池。
func NewDB(pool *pgxpool.Pool) *DB {
	if pool == nil {
		return nil
	}
	return &DB{pool: pool, root: sqlc.New(pool)}
}

// Pool 返回底层连接池；未装配时返回 nil。
func (d *DB) Pool() *pgxpool.Pool {
	if d == nil {
		return nil
	}
	return d.pool
}

// Queries 返回当前上下文应使用的查询集：优先取事务实例，否则回退到根连接池。
func (d *DB) Queries(ctx context.Context) *sqlc.Queries {
	if q, ok := ctx.Value(ctxQueriesKey{}).(*sqlc.Queries); ok && q != nil {
		return q
	}
	if d == nil {
		return nil
	}
	return d.root
}

// Ping 探测数据库连通性。
func (d *DB) Ping(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return ErrNotInitialised
	}
	return d.pool.Ping(ctx)
}

// Close 释放连接池。
func (d *DB) Close() error {
	if d == nil || d.pool == nil {
		return nil
	}
	d.pool.Close()
	return nil
}

// Shutdown 释放连接池，满足 do.ShutdownerWithContextAndError 契约。
func (d *DB) Shutdown(_ context.Context) error {
	return d.Close()
}

// ctxQueriesKey 是 context 中存放事务查询集的私有键类型。
type ctxQueriesKey struct{}

// ctxCommitHooksKey 是 context 中存放事务提交后回调的私有键类型。
type ctxCommitHooksKey struct{}

// commitHooks 承载同一次事务内登记的提交后回调。
type commitHooks struct {
	fns []func()
}

// AfterCommit 登记一个仅在本层事务成功提交后执行的回调。
//
// 注意：本函数非线程安全，只能在事务闭包所在 goroutine 内调用。
//
// 嵌套事务共用最外层事务的提交点：本函数只做登记，真正的执行交给打开事务的那一次
// RunInTx。当前不在事务中时立即执行——此时不存在事务边界，立即执行与"提交后"等价。
//
// 用途：数据库之外的副作用（如删除被替换的物理文件）无法随事务回滚。若在闭包内直接执行，
// 外层事务一旦回滚就会留下指向已删文件的悬空记录；登记为提交后回调才是安全的顺序。
//
// 并发前提：登记走的是事务内共享切片，只能在开启事务的那个 goroutine 上调用。
func AfterCommit(ctx context.Context, fn func()) {
	if fn == nil {
		return
	}
	if hooks, ok := ctx.Value(ctxCommitHooksKey{}).(*commitHooks); ok && hooks != nil {
		hooks.fns = append(hooks.fns, fn)
		return
	}
	fn()
}

// RunInTx 在事务中执行 fn，并把事务绑定的查询集显式交给闭包。
//
// 若当前上下文已处于事务中，则复用当前事务与查询集，不再开启嵌套事务；
// 这也让上层服务组合下层服务时天然幂等。经 AfterCommit 登记的回调只在真正
// 打开并提交事务的这一层执行，回滚时一律丢弃。
func (d *DB) RunInTx(ctx context.Context, fn func(ctx context.Context, q *sqlc.Queries) error) error {
	if q, ok := ctx.Value(ctxQueriesKey{}).(*sqlc.Queries); ok && q != nil {
		return fn(ctx, q)
	}
	if d == nil || d.pool == nil {
		return ErrNotInitialised
	}

	hooks := &commitHooks{}
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		txQueries := d.root.WithTx(tx)
		txCtx := context.WithValue(ctx, ctxQueriesKey{}, txQueries)
		txCtx = context.WithValue(txCtx, ctxCommitHooksKey{}, hooks)
		return fn(txCtx, txQueries)
	})
	if err != nil {
		// 事务已回滚，提交后回调必须作废。
		return err
	}

	// 事务已提交，此刻执行数据库之外的副作用才是安全的。
	for _, hook := range hooks.fns {
		hook()
	}
	return nil
}
