// Package database 负责 PostgreSQL 连接池管理、事务工作单元与类型安全数据访问。
package database

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/multitracer"
	"github.com/jackc/pgx/v5/pgxpool"

	"cgin/internal/config"
)

// Open 建立 PostgreSQL 连接池并执行连通性探测。
func Open(ctx context.Context, cfg config.DatabaseConfig, applicationName string) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("database: parse DB_URL failed: %w", err)
	}

	// 设置单条连接建立超时
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = make(map[string]string, 4)
	}
	// 会话时区固定为 UTC，保持服务端时间计算口径一致
	poolCfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	if applicationName != "" {
		poolCfg.ConnConfig.RuntimeParams["application_name"] = applicationName
	}

	// 设置会话级语句与事务超时保护
	if cfg.StatementTimeout > 0 {
		poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = millis(cfg.StatementTimeout)
	}
	if cfg.IdleInTxTimeout > 0 {
		poolCfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = millis(cfg.IdleInTxTimeout)
	}

	// 池容量与连接寿命：MinConns 为常驻热连接数，上限由配置校验保证不溢出。
	poolCfg.MaxConns = int32(cfg.MaxOpenConns) //nolint:gosec // 上限由 DatabaseConfig.Validate 保证
	poolCfg.MinConns = int32(cfg.MaxIdleConns) //nolint:gosec // 上限由 DatabaseConfig.Validate 保证
	poolCfg.MaxConnLifetime = cfg.ConnMaxLifetime
	poolCfg.MaxConnIdleTime = cfg.ConnMaxIdleTime

	// 链路追踪与结构化日志：otelpgx 负责 span，ZapTracer 负责慢查询与脱敏审计。
	poolCfg.ConnConfig.Tracer = multitracer.New(
		otelpgx.NewTracer(),
		NewZapTracer(cfg),
	)

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("database: create pool failed: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: connect failed: %w", err)
	}
	return pool, nil
}

// BudgetWarning 检查连接池上限与服务端 max_connections 预算，超出建议比例时返回告警信息。
func BudgetWarning(ctx context.Context, pool *pgxpool.Pool, maxOpenConns int) (string, error) {
	if pool == nil {
		return "", ErrNotInitialised
	}

	var maxConns, reserved int
	if err := pool.QueryRow(ctx,
		"select current_setting('max_connections')::int, current_setting('superuser_reserved_connections')::int",
	).Scan(&maxConns, &reserved); err != nil {
		return "", fmt.Errorf("database: read connection budget failed: %w", err)
	}

	usable := maxConns - reserved
	if usable <= 0 || maxOpenConns*3 <= usable {
		return "", nil
	}

	return fmt.Sprintf(
		"pool max open conns %d exceeds one third of available server budget %d (max_connections=%d, superuser_reserved=%d): "+
			"please reduce DB_MAX_OPEN_CONNS if database is shared",
		maxOpenConns, usable, maxConns, reserved), nil
}

// millis 将时长格式化为 PostgreSQL 毫秒数字符串。
func millis(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10)
}
