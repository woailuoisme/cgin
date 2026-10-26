package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// DatabaseConfig 描述 PostgreSQL 连接池参数（对齐 Laravel config/database.php）。
type DatabaseConfig struct {
	URL              string        `env:"DB_URL,required,notEmpty"`
	MaxOpenConns     int           `env:"DB_MAX_OPEN_CONNS" envDefault:"10"`
	MinConns         int           `env:"DB_MIN_CONNS" envDefault:"2"`
	ConnMaxLifetime  time.Duration `env:"DB_CONN_MAX_LIFETIME" envDefault:"30m"`
	ConnMaxIdleTime  time.Duration `env:"DB_CONN_MAX_IDLE_TIME" envDefault:"5m"`
	ConnectTimeout   time.Duration `env:"DB_CONNECT_TIMEOUT" envDefault:"5s"`
	StatementTimeout time.Duration `env:"DB_STATEMENT_TIMEOUT" envDefault:"10s"`
	IdleInTxTimeout  time.Duration `env:"DB_IDLE_IN_TX_TIMEOUT" envDefault:"30s"`
	SlowThreshold    time.Duration `env:"DB_SLOW_THRESHOLD" envDefault:"200ms"`
	Debug            bool          `env:"DB_DEBUG"`
}

// SeedConfig 描述初始管理员播种凭据。
type SeedConfig struct {
	AdminEmail    string `env:"SEED_ADMIN_EMAIL" envDefault:"admin@cgin.local"`
	AdminPassword string `env:"SEED_ADMIN_PASSWORD"`
	AdminName     string `env:"SEED_ADMIN_NAME" envDefault:"Administrator"`
}

// ResolveDBDebug 报告是否开启数据库 SQL 调试输出，优先显式配置，其次回退至 APP_DEBUG。
func (c *Config) ResolveDBDebug() bool {
	return c.Database.Debug || c.App.Debug
}

// validateDatabase 检查连接池参数与会话超时区间。
//
// DB_URL 在 env tag 上已声明 required,notEmpty；此处仍做一次 trim 后的判定，
// 因为 tag 不裁剪空白，"   " 这类值只在语义期才能拦下。
func (c *Config) validateDatabase() error {
	if strings.TrimSpace(c.Database.URL) == "" {
		return errors.New("config: DB_URL is required")
	}
	db := c.Database
	if db.MaxOpenConns <= 0 {
		return errors.New("config: DB_MAX_OPEN_CONNS must be positive")
	}
	if db.MinConns < 0 || db.MinConns > db.MaxOpenConns {
		return fmt.Errorf("config: DB_MIN_CONNS must be between 0 and DB_MAX_OPEN_CONNS(%d), got %d",
			db.MaxOpenConns, db.MinConns)
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
