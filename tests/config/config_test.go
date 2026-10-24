// Package config_test 以黑盒方式验证 gin 运行模式的推导规则。
//
// 规则的关键不在于取值本身，而在于方向：只有明确声明的本地环境才允许 debug，
// 未知的 APP_ENV 必须落到 release，否则会把完整路由表打进非本地环境的日志。
package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
)

// newConfig 构造只填了模式相关字段的配置，避免依赖真实环境变量。
func newConfig(env, override string) *config.Config {
	return &config.Config{App: config.AppConfig{Env: env, GinMode: override}}
}

// TestResolveGinMode 覆盖推导与显式覆盖两条路径。
func TestResolveGinMode(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		override string
		want     string
	}{
		{"本地用 debug", "local", "", config.ModeDebug},
		{"development 同样用 debug", "development", "", config.ModeDebug},
		{"生产用 release", "production", "", config.ModeRelease},
		{"预发用 release", "staging", "", config.ModeRelease},
		{"未知环境一律 release", "producton", "", config.ModeRelease},
		{"空环境一律 release", "", "", config.ModeRelease},
		{"显式覆盖优先于推导", "local", config.ModeRelease, config.ModeRelease},
		{"覆盖可用 test", "production", config.ModeTest, config.ModeTest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newConfig(tt.env, tt.override).ResolveGinMode())
		})
	}
}

// TestModePredicates 锁定 IsLocal 与 IsProduction 的边界，二者对同一环境不会同时为真。
func TestModePredicates(t *testing.T) {
	tests := []struct {
		env        string
		wantLocal  bool
		wantIsProd bool
	}{
		{"local", true, false},
		{"development", true, false},
		{"production", false, true},
		{"staging", false, false},
		{"", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			cfg := newConfig(tt.env, "")
			assert.Equal(t, tt.wantLocal, cfg.IsLocal())
			assert.Equal(t, tt.wantIsProd, cfg.IsProduction())
			assert.False(t, cfg.IsLocal() && cfg.IsProduction(), "两个判定不应同时为真")
		})
	}
}

// TestResolveDBDebug 断言数据库调试模式的推导规则（显式优先，联动 APP_DEBUG 兜底）。
func TestResolveDBDebug(t *testing.T) {
	tests := []struct {
		name     string
		appDebug bool
		dbDebug  bool
		want     bool
	}{
		{"全部关闭", false, false, false},
		{"仅开启 APP_DEBUG", true, false, true},
		{"仅开启 DB_DEBUG", false, true, true},
		{"全部开启", true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				App:      config.AppConfig{Debug: tt.appDebug},
				Database: config.DatabaseConfig{Debug: tt.dbDebug},
			}
			assert.Equal(t, tt.want, cfg.ResolveDBDebug())
		})
	}
}

// TestSearchConfigEnabled 断言全文检索启用的判定逻辑。
func TestSearchConfigEnabled(t *testing.T) {
	tests := []struct {
		name   string
		driver string
		host   string
		want   bool
	}{
		{"驱动非 meilisearch", "noop", "http://localhost:7700", false},
		{"空主机地址", "meilisearch", "", false},
		{"纯空白主机地址", "meilisearch", "   ", false},
		{"合法配置正常启用", "meilisearch", "http://127.0.0.1:7700", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.SearchConfig{
				Driver: tt.driver,
				Meilisearch: config.MeilisearchConfig{
					Host: tt.host,
				},
			}
			assert.Equal(t, tt.want, cfg.Enabled())
		})
	}
}

// TestLoadValidationErrors 验证通过环境变量注入非法参数时的各模块校验拦截。
func TestLoadValidationErrors(t *testing.T) {
	setBaseValidEnv := func(t *testing.T) {
		t.Helper()
		t.Setenv("APP_ENV", "local")
		t.Setenv("DB_URL", "postgres://user:pass@localhost:5432/db")
		t.Setenv("REDIS_URL", "redis://localhost:6379")
		t.Setenv("JWT_SECRET", "this-is-a-very-long-secret-key-with-more-than-32-chars")
		t.Setenv("JWT_TTL", "15m")
		t.Setenv("JWT_REFRESH_TTL", "720h")
		t.Setenv("FILESYSTEM_DISK", "local")
		t.Setenv("STORAGE_LOCAL_ROOT", "./storage/app/uploads")
	}

	t.Run("非法 APP_ENV", func(t *testing.T) {
		setBaseValidEnv(t)
		t.Setenv("APP_ENV", "invalid_cluster_env")
		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "APP_ENV")
	})

	t.Run("短 JWT_SECRET", func(t *testing.T) {
		setBaseValidEnv(t)
		t.Setenv("JWT_SECRET", "short-secret")
		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "JWT_SECRET")
	})

	t.Run("JWT_REFRESH_TTL 不大于 JWT_TTL", func(t *testing.T) {
		setBaseValidEnv(t)
		t.Setenv("JWT_TTL", "1h")
		t.Setenv("JWT_REFRESH_TTL", "30m")
		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "JWT_REFRESH_TTL must be greater than JWT_TTL")
	})

	t.Run("非法 S3 缺失配置", func(t *testing.T) {
		setBaseValidEnv(t)
		t.Setenv("FILESYSTEM_DISK", "s3")
		t.Setenv("AWS_ENDPOINT", "")
		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "AWS_ENDPOINT is required")
	})

	t.Run("不支持的 SCOUT_DRIVER", func(t *testing.T) {
		setBaseValidEnv(t)
		t.Setenv("SCOUT_DRIVER", "elastic_search")
		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SCOUT_DRIVER")
	})

	t.Run("启用 Realtime 但 CENTRIFUGO_URL 为空", func(t *testing.T) {
		setBaseValidEnv(t)
		t.Setenv("CENTRIFUGO_ENABLED", "true")
		t.Setenv("CENTRIFUGO_URL", "")
		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "CENTRIFUGO_URL cannot be empty")
	})

	t.Run("启用 Realtime 但 CENTRIFUGO_URL 格式非法", func(t *testing.T) {
		setBaseValidEnv(t)
		t.Setenv("CENTRIFUGO_ENABLED", "true")
		t.Setenv("CENTRIFUGO_URL", ":::invalid-url")
		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid CENTRIFUGO_URL")
	})

	t.Run("启用 Realtime 但 CENTRIFUGO_SECRET 为空", func(t *testing.T) {
		setBaseValidEnv(t)
		t.Setenv("CENTRIFUGO_ENABLED", "true")
		t.Setenv("CENTRIFUGO_URL", "http://127.0.0.1:8000/api")
		t.Setenv("CENTRIFUGO_API_KEY", "test-api-key")
		t.Setenv("CENTRIFUGO_SECRET", "")
		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "CENTRIFUGO_SECRET cannot be empty")
	})

	t.Run("非法 APP_TIMEZONE", func(t *testing.T) {
		setBaseValidEnv(t)
		t.Setenv("APP_TIMEZONE", "Invalid/Timezone_Name")
		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "APP_TIMEZONE")
	})

	t.Run("非法 OTEL_TRACES_SAMPLE_RATIO", func(t *testing.T) {
		setBaseValidEnv(t)
		t.Setenv("OTEL_ENABLED", "true")
		t.Setenv("OTEL_TRACES_SAMPLE_RATIO", "1.5")
		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OTEL_TRACES_SAMPLE_RATIO")
	})

	t.Run("非法 OTEL_METRIC_INTERVAL 为负", func(t *testing.T) {
		setBaseValidEnv(t)
		t.Setenv("OTEL_ENABLED", "true")
		t.Setenv("OTEL_METRIC_INTERVAL", "-10s")
		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OTEL_METRIC_INTERVAL")
	})
}

// TestAppTimezone 测试 APP_TIMEZONE 时区解析与 Location 转换。
func TestAppTimezone(t *testing.T) {
	t.Run("默认或空时区回退到 UTC", func(t *testing.T) {
		app := config.AppConfig{Timezone: ""}
		assert.Equal(t, "UTC", app.Location().String())
	})

	t.Run("有效时区正常解析", func(t *testing.T) {
		app := config.AppConfig{Timezone: "Asia/Shanghai"}
		assert.Equal(t, "Asia/Shanghai", app.Location().String())
	})

	t.Run("非法时区回退到 UTC", func(t *testing.T) {
		app := config.AppConfig{Timezone: "Unknown/Foo"}
		assert.Equal(t, "UTC", app.Location().String())
	})
}
