package database_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"sync"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"go.uber.org/goleak"
)

type mockState struct {
	mu         sync.RWMutex
	onAnyQuery func(query string)
	execFn     func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error)
	queryFn    func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error)
}

type mockDriver struct{}

func (d *mockDriver) Open(name string) (driver.Conn, error) {
	return &mockConn{}, nil
}

type mockConnector struct {
	state *mockState
}

func (c *mockConnector) Connect(ctx context.Context) (driver.Conn, error) {
	return &mockConn{state: c.state}, nil
}

func (c *mockConnector) Driver() driver.Driver {
	return &mockDriver{}
}

type mockConn struct {
	state *mockState
}

func (c *mockConn) Prepare(query string) (driver.Stmt, error) {
	return &mockStmt{query: query, state: c.state}, nil
}

func (c *mockConn) Close() error {
	return nil
}

func (c *mockConn) Begin() (driver.Tx, error) {
	return &mockTx{}, nil
}

func (c *mockConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return &mockTx{}, nil
}

func (c *mockConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.state != nil {
		c.state.mu.RLock()
		onQ := c.state.onAnyQuery
		fn := c.state.execFn
		c.state.mu.RUnlock()
		if onQ != nil {
			onQ(query)
		}
		if fn != nil {
			return fn(ctx, query, args)
		}
	}
	return mockResult{rowsAffected: 1}, nil
}

func (c *mockConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.state != nil {
		c.state.mu.RLock()
		onQ := c.state.onAnyQuery
		fn := c.state.queryFn
		c.state.mu.RUnlock()
		if onQ != nil {
			onQ(query)
		}
		if fn != nil {
			return fn(ctx, query, args)
		}
	}
	return &mockRows{
		columns: []string{"id", "email", "name", "role", "status"},
		rows: [][]driver.Value{
			{"mock-id", "mock@example.com", "Mock User", "user", "active"},
		},
	}, nil
}

type mockStmt struct {
	query string
	state *mockState
}

func (s *mockStmt) Close() error { return nil }

func (s *mockStmt) NumInput() int { return -1 }

func (s *mockStmt) Exec(args []driver.Value) (driver.Result, error) {
	var namedArgs []driver.NamedValue
	for i, a := range args {
		namedArgs = append(namedArgs, driver.NamedValue{Ordinal: i + 1, Value: a})
	}
	return s.ExecContext(context.Background(), namedArgs)
}

func (s *mockStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if s.state != nil {
		s.state.mu.RLock()
		onQ := s.state.onAnyQuery
		fn := s.state.execFn
		s.state.mu.RUnlock()
		if onQ != nil {
			onQ(s.query)
		}
		if fn != nil {
			return fn(ctx, s.query, args)
		}
	}
	return mockResult{rowsAffected: 1}, nil
}

func (s *mockStmt) Query(args []driver.Value) (driver.Rows, error) {
	var namedArgs []driver.NamedValue
	for i, a := range args {
		namedArgs = append(namedArgs, driver.NamedValue{Ordinal: i + 1, Value: a})
	}
	return s.QueryContext(context.Background(), namedArgs)
}

func (s *mockStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if s.state != nil {
		s.state.mu.RLock()
		onQ := s.state.onAnyQuery
		fn := s.state.queryFn
		s.state.mu.RUnlock()
		if onQ != nil {
			onQ(s.query)
		}
		if fn != nil {
			return fn(ctx, s.query, args)
		}
	}
	return &mockRows{
		columns: []string{"id", "email", "name", "role", "status"},
		rows: [][]driver.Value{
			{"mock-id", "mock@example.com", "Mock User", "user", "active"},
		},
	}, nil
}

type mockTx struct{}

func (t *mockTx) Commit() error   { return nil }
func (t *mockTx) Rollback() error { return nil }

type mockResult struct {
	rowsAffected int64
	lastInsertID int64
}

func (r mockResult) LastInsertId() (int64, error) { return r.lastInsertID, nil }
func (r mockResult) RowsAffected() (int64, error) { return r.rowsAffected, nil }

type mockRows struct {
	columns []string
	rows    [][]driver.Value
	idx     int
	nextErr error
}

func (r *mockRows) Columns() []string { return r.columns }
func (r *mockRows) Close() error      { return nil }
func (r *mockRows) Next(dest []driver.Value) error {
	if r.nextErr != nil {
		return r.nextErr
	}
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

// TestDB 是用于隔离测试的 Bun DB 句柄，内嵌底层 mockState 支持并发安全的执行行为定制。
type TestDB struct {
	*bun.DB
	sqlDB *sql.DB
	state *mockState
}

// SetOnAnyQuery 注册 SQL 执行监听回调。
func (tdb *TestDB) SetOnAnyQuery(fn func(query string)) {
	tdb.state.mu.Lock()
	tdb.state.onAnyQuery = fn
	tdb.state.mu.Unlock()
}

// SetMockExec 自定义 Exec 执行模拟。
func (tdb *TestDB) SetMockExec(fn func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error)) {
	tdb.state.mu.Lock()
	tdb.state.execFn = fn
	tdb.state.mu.Unlock()
}

// SetMockQuery 自定义 Query 查询模拟。
func (tdb *TestDB) SetMockQuery(fn func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error)) {
	tdb.state.mu.Lock()
	tdb.state.queryFn = fn
	tdb.state.mu.Unlock()
}

// BunDB 返回内嵌的底层 *bun.DB 实例，满足 UnwrapBunDB 接口。
func (tdb *TestDB) BunDB() *bun.DB {
	return tdb.DB
}

// Unwrap 返回内嵌的底层 *bun.DB 实例，满足标准解包接口。
func (tdb *TestDB) Unwrap() *bun.DB {
	return tdb.DB
}

func makeMockRows(columns []string, rows ...[]driver.Value) driver.Rows {
	return &mockRows{
		columns: columns,
		rows:    rows,
	}
}

func makeMockRowsWithErr(columns []string, nextErr error) driver.Rows {
	return &mockRows{
		columns: columns,
		nextErr: nextErr,
	}
}

func setupMockDB(t *testing.T) *TestDB {
	t.Helper()
	state := &mockState{}
	connector := &mockConnector{state: state}
	sqlDB := sql.OpenDB(connector)

	tdb := &TestDB{
		DB:    bun.NewDB(sqlDB, pgdialect.New()),
		sqlDB: sqlDB,
		state: state,
	}

	t.Cleanup(func() {
		tdb.state.mu.Lock()
		tdb.state.onAnyQuery = nil
		tdb.state.execFn = nil
		tdb.state.queryFn = nil
		tdb.state.mu.Unlock()
		_ = sqlDB.Close()
	})

	return tdb
}

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
