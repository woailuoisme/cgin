// Package database 负责 PostgreSQL 连接池管理与客户端初始化。
package database

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"bgin/internal/config"
)

// Open 建立 PostgreSQL 连接池并执行连通性探测。
func Open(ctx context.Context, cfg config.DatabaseConfig, applicationName string) (*bun.DB, error) {
	pgxConfig, err := pgx.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("database: parse DB_URL failed: %w", err)
	}

	// 设置单条连接建立超时
	pgxConfig.ConnectTimeout = cfg.ConnectTimeout

	if pgxConfig.RuntimeParams == nil {
		pgxConfig.RuntimeParams = make(map[string]string, 4)
	}
	// 会话时区固定为 UTC，保持服务端时间计算口径一致
	pgxConfig.RuntimeParams["timezone"] = "UTC"
	if applicationName != "" {
		pgxConfig.RuntimeParams["application_name"] = applicationName
	}

	// 设置会话级语句与事务超时保护
	if cfg.StatementTimeout > 0 {
		pgxConfig.RuntimeParams["statement_timeout"] = millis(cfg.StatementTimeout)
	}
	if cfg.IdleInTxTimeout > 0 {
		pgxConfig.RuntimeParams["idle_in_transaction_session_timeout"] = millis(cfg.IdleInTxTimeout)
	}

	sqlDB := stdlib.OpenDB(*pgxConfig)
	// 设置连接池容量与生命周期参数
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("database: connect failed: %w", err)
	}

	db := bun.NewDB(sqlDB, pgdialect.New())
	db = AddQueryHooks(db, cfg)
	return db, nil
}

// BudgetWarning 检查连接池上限与服务端 max_connections 预算，超出建议比例时返回告警信息。
func BudgetWarning(ctx context.Context, db *bun.DB, maxOpenConns int) (string, error) {
	var maxConns, reserved int
	if err := db.QueryRowContext(ctx,
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
