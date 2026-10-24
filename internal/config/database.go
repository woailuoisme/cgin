package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// DatabaseConfig 描述 PostgreSQL 连接池参数（对齐 Laravel config/database.php）。
type DatabaseConfig struct {
	URL              string        `mapstructure:"db_url"`
	MaxOpenConns     int           `mapstructure:"db_max_open_conns"`
	MaxIdleConns     int           `mapstructure:"db_max_idle_conns"`
	ConnMaxLifetime  time.Duration `mapstructure:"db_conn_max_lifetime"`
	ConnMaxIdleTime  time.Duration `mapstructure:"db_conn_max_idle_time"`
	ConnectTimeout   time.Duration `mapstructure:"db_connect_timeout"`
	StatementTimeout time.Duration `mapstructure:"db_statement_timeout"`
	IdleInTxTimeout  time.Duration `mapstructure:"db_idle_in_tx_timeout"`
	SlowThreshold    time.Duration `mapstructure:"db_slow_threshold"`
	Debug            bool          `mapstructure:"db_debug"`
}

// SeedConfig 描述初始管理员播种凭据。
type SeedConfig struct {
	AdminEmail    string `mapstructure:"seed_admin_email"`
	AdminPassword string `mapstructure:"seed_admin_password"`
	AdminName     string `mapstructure:"seed_admin_name"`
}

// ResolveDBDebug 报告是否开启数据库 SQL 调试输出，优先显式配置，其次回退至 APP_DEBUG。
func (c *Config) ResolveDBDebug() bool {
	return c.Database.Debug || c.App.Debug
}

// validateDatabase 检查连接池参数与会话超时区间。
func (c *Config) validateDatabase() error {
	if strings.TrimSpace(c.Database.URL) == "" {
		return errors.New("config: DB_URL is required")
	}
	db := c.Database
	if db.MaxOpenConns <= 0 {
		return errors.New("config: DB_MAX_OPEN_CONNS must be positive")
	}
	if db.MaxIdleConns < 0 || db.MaxIdleConns > db.MaxOpenConns {
		return fmt.Errorf("config: DB_MAX_IDLE_CONNS must be between 0 and DB_MAX_OPEN_CONNS(%d), got %d",
			db.MaxOpenConns, db.MaxIdleConns)
	}
	if db.ConnectTimeout <= 0 {
		return errors.New("config: DB_CONNECT_TIMEOUT must be positive")
	}

	for _, item := range []struct {
		name  string
		value time.Duration
	}{
		{"DB_STATEMENT_TIMEOUT", db.StatementTimeout},
		{"DB_IDLE_IN_TX_TIMEOUT", db.IdleInTxTimeout},
	} {
		if item.value < 0 || (item.value > 0 && item.value < time.Millisecond) {
			return fmt.Errorf("config: %s must be 0 (server default) or at least 1ms, got %s", item.name, item.value)
		}
	}

	if db.SlowThreshold < 0 {
		return fmt.Errorf("config: DB_SLOW_THRESHOLD cannot be negative: %s", db.SlowThreshold)
	}
	return nil
}
