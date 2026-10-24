//go:build integration

// 本文件对着真实 Postgres 锁定连接池与会话参数。
//
// 这些设置的特点是"静默生效"：写错不会报错，只会在连接被服务端拒绝、或排查慢查询与
// 锁等待时才发现——那时已经离原因很远了。所以每一项都要有断言。
package integration_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/database"
)

// TestDatabasePoolLimits 断言池上限真的落到了 sql.DB 上。
func TestDatabasePoolLimits(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err, "加载配置失败")

	db, err := database.Open(t.Context(), cfg.Database, "bgin-tests")
	require.NoError(t, err, "请确认 DB_URL 可达")
	t.Cleanup(func() { _ = db.Close() })

	assert.Equal(t, cfg.Database.MaxOpenConns, db.Stats().MaxOpenConnections,
		"池上限决定了本实例占用服务端多少连接槽位，必须与配置一致")
}

// TestDatabaseSessionParams 断言会话参数由客户端设定，而不是随服务端默认走。
//
// 用同一条连接反查服务端：application_name 是共享 Postgres 上唯一能区分连接归属的字段，
// timezone 决定时间函数与文本输出的口径——两者都影响排查时的可读性。
func TestDatabaseSessionParams(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err, "加载配置失败")

	const appName = "bgin-tests"
	db, err := database.Open(t.Context(), cfg.Database, appName)
	require.NoError(t, err, "请确认 DB_URL 可达")
	t.Cleanup(func() { _ = db.Close() })

	var gotAppName, gotTimeZone, gotStatementTimeout, gotIdleInTx string
	require.NoError(t, db.QueryRowContext(t.Context(),
		`select current_setting('application_name'),
		        current_setting('TimeZone'),
		        current_setting('statement_timeout'),
		        current_setting('idle_in_transaction_session_timeout')`,
	).Scan(&gotAppName, &gotTimeZone, &gotStatementTimeout, &gotIdleInTx))

	assert.Equal(t, appName, gotAppName)
	assert.Equal(t, "UTC", gotTimeZone)

	// 两个超时都得真的下发。这里只断言"不是服务端的关闭态"，具体值由下面的用例锁定：
	// 服务端会把毫秒规范化成 30s / 1min 这类形式，写死期望值会让用例随配置变动而失败。
	if cfg.Database.StatementTimeout > 0 {
		assert.NotEqual(t, "0", gotStatementTimeout, "配了预算就必须真的下发")
	}
	if cfg.Database.IdleInTxTimeout > 0 {
		assert.NotEqual(t, "0", gotIdleInTx, "配了预算就必须真的下发")
	}
}

// TestDatabaseSessionIsVisibleToPgStatActivity 确认应用名能被 DBA 侧的视图看到。
//
// 这是 application_name 存在的全部意义：共享 Postgres 上，
// select application_name, count(*) from pg_stat_activity group by 1 应当能认出谁是谁。
func TestDatabaseSessionIsVisibleToPgStatActivity(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err, "加载配置失败")

	db, err := database.Open(t.Context(), cfg.Database, "bgin-tests")
	require.NoError(t, err, "请确认 DB_URL 可达")
	t.Cleanup(func() { _ = db.Close() })

	var seen string
	require.NoError(t, db.QueryRowContext(t.Context(),
		"select application_name from pg_stat_activity where pid = pg_backend_pid()",
	).Scan(&seen))
	assert.Equal(t, "bgin-tests", seen)
}

// TestDatabaseConnectTimeoutIsEnforced 确认不可达的服务端会在超时预算内失败，
// 而不是把调用方挂到它自己的超时为止。
func TestDatabaseConnectTimeoutIsEnforced(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err, "加载配置失败")

	// 10.255.255.1 是私网保留地址：包会被丢弃而不是立刻拒绝，正好用来观察超时。
	cfg.Database.URL = "postgres://user:pass@10.255.255.1:5432/bgin?sslmode=disable"
	cfg.Database.ConnectTimeout = time.Second

	start := time.Now()
	_, err = database.Open(t.Context(), cfg.Database, "bgin-tests")
	elapsed := time.Since(start)

	require.Error(t, err, "连不上时必须失败，而不是无限等待")
	// 1 秒预算 + 重试的余量：若超时被摘掉，这条连接会一直挂在拨号上。
	assert.Less(t, elapsed, 5*time.Second, "失败应当发生在连接超时预算附近，实际耗时 %s", elapsed)
}

// TestDatabaseStatementTimeoutIsEnforced 确认会话级查询超时真的会掐掉跑飞的查询。
//
// 只断言参数下发成功是不够的：这条保护的全部价值在于服务端确实会取消语句，
// 并且是在预算附近取消，而不是等它自己跑完。
func TestDatabaseStatementTimeoutIsEnforced(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err, "加载配置失败")

	cfg.Database.StatementTimeout = time.Second
	db, err := database.Open(t.Context(), cfg.Database, "bgin-tests")
	require.NoError(t, err, "请确认 DB_URL 可达")
	t.Cleanup(func() { _ = db.Close() })

	var got string
	require.NoError(t, db.QueryRowContext(t.Context(),
		"select current_setting('statement_timeout')").Scan(&got))
	assert.Equal(t, "1s", got, "按毫秒下发，服务端规范化后回读为 1s")

	start := time.Now()
	_, err = db.ExecContext(t.Context(), "select pg_sleep(5)")
	elapsed := time.Since(start)

	require.Error(t, err, "超时必须让长查询失败，否则连接会被它一直占着")
	var pgErr *pgconn.PgError
	if assert.ErrorAs(t, err, &pgErr) {
		assert.Equal(t, "57014", pgErr.Code, "应当是 query_canceled")
	}
	assert.Less(t, elapsed, 4*time.Second, "应当在 1 秒预算附近返回，实际耗时 %s", elapsed)
}

// TestDatabaseBudgetWarning 确认连接预算诊断既不漏报也不误报。
//
// 它拦的是"池上限配得比服务端给得起的大"这类错：在共享 Postgres 上，
// 这种错的代价是峰值时业务请求拿到 too many clients。
func TestDatabaseBudgetWarning(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err, "加载配置失败")

	db, err := database.Open(t.Context(), cfg.Database, "bgin-tests")
	require.NoError(t, err, "请确认 DB_URL 可达")
	t.Cleanup(func() { _ = db.Close() })

	t.Run("池上限吃掉服务端预算时告警", func(t *testing.T) {
		warning, err := database.BudgetWarning(t.Context(), db, 10_000)
		require.NoError(t, err)
		assert.Contains(t, warning, "10000")
		assert.Contains(t, warning, "DB_MAX_OPEN_CONNS", "告警要说清该调哪个配置")
	})

	t.Run("默认池上限不告警", func(t *testing.T) {
		// 这台服务端本身的预算就很小的话，告警才是正确行为，跳过而不是判失败。
		var usable int
		require.NoError(t, db.QueryRowContext(t.Context(),
			`select current_setting('max_connections')::int
			      - current_setting('superuser_reserved_connections')::int`,
		).Scan(&usable))
		if cfg.Database.MaxOpenConns*3 > usable {
			t.Skipf("服务端可用预算只有 %d 条，本实例的池上限本就应当告警", usable)
		}

		warning, err := database.BudgetWarning(t.Context(), db, cfg.Database.MaxOpenConns)
		require.NoError(t, err)
		assert.Empty(t, warning)
	})
}
