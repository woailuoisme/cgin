package config

import (
	"cmp"
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

// Validate 检查连接池参数与会话超时区间。
func (db *DatabaseConfig) Validate() error {
	if strings.TrimSpace(db.URL) == "" {
		return errors.New("config: DB_URL is required")
	}
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

// SeedConfig 描述初始管理员播种凭据。
type SeedConfig struct {
	AdminEmail    string `env:"SEED_ADMIN_EMAIL" envDefault:"admin@cgin.local"`
	AdminPassword string `env:"SEED_ADMIN_PASSWORD"`
	AdminName     string `env:"SEED_ADMIN_NAME" envDefault:"Administrator"`
}

// CacheConfig 描述 Redis/Valkey 缓存连接参数（对齐 Laravel config/cache.php）。
type CacheConfig struct {
	URL string `env:"REDIS_URL,required,notEmpty"`
}

// Validate 检查缓存连接配置。
func (c *CacheConfig) Validate() error {
	if strings.TrimSpace(c.URL) == "" {
		return errors.New("config: REDIS_URL is required")
	}
	return nil
}

// 文件存储驱动常量。
const (
	StorageDriverLocal = "local"
	StorageDriverS3    = "s3"
)

// StorageConfig 描述存储磁盘选择与参数（对齐 Laravel config/filesystems.php）。
type StorageConfig struct {
	Driver string `env:"FILESYSTEM_DISK" envDefault:"local"`
	Local  LocalStorageConfig
	S3     S3StorageConfig
}

// LocalStorageConfig 描述本地文件系统存储驱动。
type LocalStorageConfig struct {
	Root string `env:"STORAGE_LOCAL_ROOT" envDefault:"./storage/app/uploads"`
	URL  string `env:"STORAGE_LOCAL_URL"`
}

// S3StorageConfig 描述 S3 兼容对象存储配置。
type S3StorageConfig struct {
	Endpoint        string `env:"S3_ENDPOINT"`
	Bucket          string `env:"S3_BUCKET"`
	Region          string `env:"S3_REGION" envDefault:"garage"`
	AccessKeyID     string `env:"S3_ACCESS_KEY"`
	SecretAccessKey string `env:"S3_SECRET_KEY"`
	UseSSL          bool   `env:"S3_USE_SSL" envDefault:"false"`
	PublicURL       string `env:"S3_PUBLIC_URL"`
}

// Validate 检查存储驱动合法性与必填字段。
func (s *StorageConfig) Validate() error {
	switch s.Driver {
	case StorageDriverLocal:
		if strings.TrimSpace(s.Local.Root) == "" {
			return errors.New("config: STORAGE_LOCAL_ROOT cannot be empty when FILESYSTEM_DISK=local")
		}
	case StorageDriverS3:
		for _, item := range []struct{ name, value string }{
			{"S3_ENDPOINT", s.S3.Endpoint},
			{"S3_BUCKET", s.S3.Bucket},
			{"S3_ACCESS_KEY", s.S3.AccessKeyID},
			{"S3_SECRET_KEY", s.S3.SecretAccessKey},
		} {
			if strings.TrimSpace(item.value) == "" {
				return fmt.Errorf("config: %s is required when FILESYSTEM_DISK=s3", item.name)
			}
		}
	default:
		return fmt.Errorf("config: FILESYSTEM_DISK=%q is not a valid value, options: %s / %s",
			s.Driver, StorageDriverLocal, StorageDriverS3)
	}
	return nil
}

// QueueConfig 描述异步队列与并发参数（对齐 Laravel config/queue.php）。
type QueueConfig struct {
	RedisURL        string        `env:"QUEUE_REDIS_URL"`
	Concurrency     int           `env:"QUEUE_CONCURRENCY" envDefault:"10"`
	ShutdownTimeout time.Duration `env:"QUEUE_SHUTDOWN_TIMEOUT" envDefault:"3s"`
}

// Normalize 缺省 Redis URL 时回退至全局缓存连接，并填充零值默认参数。
func (q *QueueConfig) Normalize(fallbackRedisURL string) {
	q.RedisURL = cmp.Or(strings.TrimSpace(q.RedisURL), fallbackRedisURL)
	if q.Concurrency <= 0 {
		q.Concurrency = 10
	}
	if q.ShutdownTimeout <= 0 {
		q.ShutdownTimeout = 3 * time.Second
	}
}

// EventBusConfig 描述事件总线与 CQRS 路由参数。
type EventBusConfig struct {
	Driver      string `env:"EVENTBUS_DRIVER" envDefault:"go_channel"`
	TopicPrefix string `env:"EVENTBUS_TOPIC_PREFIX" envDefault:"cgin"`
	// LogLevel 控制事件总线 Watermill 内部日志输出级别（如 "warn"、"error"、"info"），默认 "warn" 以降低路由流水噪音。
	LogLevel   string `env:"EVENTBUS_LOG_LEVEL" envDefault:"warn"`
	BufferSize int    `env:"EVENTBUS_BUFFER_SIZE" envDefault:"2048"`
}

// Normalize 填充事件总线零值默认参数。
func (e *EventBusConfig) Normalize() {
	e.Driver = cmp.Or(strings.TrimSpace(e.Driver), "go_channel")
	e.TopicPrefix = cmp.Or(strings.TrimSpace(e.TopicPrefix), defaultAppName)
	e.LogLevel = cmp.Or(strings.TrimSpace(e.LogLevel), "warn")
	if e.BufferSize <= 0 {
		e.BufferSize = 2048
	}
}

// SearchConfig 描述全文检索驱动与连接参数（对齐 Laravel config/scout.php）。
type SearchConfig struct {
	Driver      string `env:"SCOUT_DRIVER" envDefault:"noop"`
	Prefix      string `env:"SCOUT_PREFIX"`
	Meilisearch MeilisearchConfig
}

// MeilisearchConfig 描述 Meilisearch 连接与鉴权凭据。
type MeilisearchConfig struct {
	Host   string `env:"MEILISEARCH_HOST" envDefault:"http://127.0.0.1:7700"`
	APIKey string `env:"MEILISEARCH_KEY"`
}

// Enabled 报告是否启用了实际的搜索驱动服务。
func (s *SearchConfig) Enabled() bool {
	return s.Driver == "meilisearch" && strings.TrimSpace(s.Meilisearch.Host) != ""
}

// Validate 检查全文检索驱动配置。
func (s *SearchConfig) Validate() error {
	switch s.Driver {
	case "", "noop", "meilisearch":
		return nil
	default:
		return fmt.Errorf("config: SCOUT_DRIVER=%q is not supported, options: noop / meilisearch", s.Driver)
	}
}

// GeoIPConfig 描述基于 ip2region 的离线 IP 归属地库配置。
type GeoIPConfig struct {
	Enabled bool   `env:"GEOIP_ENABLED"`
	DBPath  string `env:"GEOIP_DB_PATH" envDefault:"./storage/app/geoip/ip2region_v4.xdb"`
}

// TelemetryConfig 描述 OpenTelemetry 遥测与全链路追踪配置。
type TelemetryConfig struct {
	Enabled        bool          `env:"OTEL_ENABLED"`
	Endpoint       string        `env:"OTEL_EXPORTER_OTLP_ENDPOINT" envDefault:"http://127.0.0.1:4317"`
	Insecure       bool          `env:"OTEL_INSECURE" envDefault:"true"`
	ServiceName    string        `env:"OTEL_SERVICE_NAME"`
	SampleRatio    float64       `env:"OTEL_TRACES_SAMPLE_RATIO" envDefault:"1.0"`
	MetricInterval time.Duration `env:"OTEL_METRIC_INTERVAL" envDefault:"15s"`
}

// Normalize 归一化遥测服务名、Collector 地址与采集间隔。
func (t *TelemetryConfig) Normalize(appName string) {
	t.ServiceName = cmp.Or(strings.TrimSpace(t.ServiceName), strings.TrimSpace(appName), defaultAppName)
	t.Endpoint = cmp.Or(strings.TrimSpace(t.Endpoint), "http://127.0.0.1:4317")
	if t.MetricInterval == 0 {
		t.MetricInterval = 15 * time.Second
	}
}

// Validate 校验 Telemetry 配置项语义合法性。
func (t *TelemetryConfig) Validate() error {
	if !t.Enabled {
		return nil
	}
	if t.SampleRatio < 0.0 || t.SampleRatio > 1.0 {
		return fmt.Errorf("config: OTEL_TRACES_SAMPLE_RATIO must be between 0.0 and 1.0, got %f", t.SampleRatio)
	}
	if t.MetricInterval < 0 {
		return fmt.Errorf("config: OTEL_METRIC_INTERVAL cannot be negative, got %s", t.MetricInterval)
	}
	return nil
}
