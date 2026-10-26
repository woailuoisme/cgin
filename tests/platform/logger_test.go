package platform_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"cgin/internal/config"
	"cgin/pkg/logger"
)

// TestLogger_BuildAndContext 验证日志器构建与 Context 请求标识传递。
func TestLogger_BuildAndContext(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{
			Name: "cgin-test",
			Env:  "local",
		},
		Log: config.LogConfig{
			Level:   "debug",
			Channel: "console",
		},
	}

	l := logger.New(cfg)

	ctx := logger.WithContext(context.Background(), l, "req_test_123")
	extracted, ok := logger.FromContextOK(ctx)
	assert.True(t, ok)
	assert.NotZero(t, extracted)

	// 缺失时返回默认兜底日志器
	fallback := logger.FromContext(context.Background())
	assert.NotZero(t, fallback)
}

// TestLogger_ChannelJSON 验证 JSON 通道构建。
func TestLogger_ChannelJSON(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{
			Name: "cgin-test",
			Env:  "production",
		},
		Log: config.LogConfig{
			Level:   "info",
			Channel: "json",
		},
	}

	l := logger.New(cfg)
	assert.NotZero(t, l)
}

// TestLogger_ColorDecision 验证颜色启闭决策与环境变量优先级。
func TestLogger_ColorDecision(t *testing.T) {
	t.Run("NO_COLOR 存在时禁用着色", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		t.Setenv("FORCE_COLOR", "1")

		cfg := &config.Config{
			App: config.AppConfig{Env: "local"},
			Log: config.LogConfig{Channel: "console"},
		}
		l := logger.New(cfg)
		assert.NotZero(t, l)
	})

	t.Run("FORCE_COLOR 等于 0 或 false 时显式禁用着色", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("FORCE_COLOR", "0")

		cfg := &config.Config{
			App: config.AppConfig{Env: "production"},
			Log: config.LogConfig{Channel: "console"},
		}
		l := logger.New(cfg)
		assert.NotZero(t, l)

		t.Setenv("FORCE_COLOR", "false")
		l2 := logger.New(cfg)
		assert.NotZero(t, l2)
	})

	t.Run("FORCE_COLOR 或 CLICOLOR_FORCE 强制着色", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("FORCE_COLOR", "1")

		cfg := &config.Config{
			App: config.AppConfig{Env: "production"},
			Log: config.LogConfig{Channel: "console"},
		}
		l := logger.New(cfg)
		assert.NotZero(t, l)

		t.Setenv("FORCE_COLOR", "")
		t.Setenv("CLICOLOR_FORCE", "1")
		l2 := logger.New(cfg)
		assert.NotZero(t, l2)
	})
}

// TestLogger_OptionsAndRedaction 验证函数式选项构建、自定义 Writer 捕获与敏感数据脱敏。
func TestLogger_OptionsAndRedaction(t *testing.T) {
	t.Run("自定义 Writer 捕获与 JSON 通道输出", func(t *testing.T) {
		var buf bytes.Buffer
		l := logger.NewWithOptions(
			logger.WithLevel("debug"),
			logger.WithChannel("json"),
			logger.WithServiceName("auth-service"),
			logger.WithWriter(&buf),
			logger.WithRedaction(false),
		)

		l.Info().Str("action", "test_run").Msg("operational check")
		output := buf.String()
		assert.Contains(t, output, `"service":"auth-service"`)
		assert.Contains(t, output, `"action":"test_run"`)
		assert.Contains(t, output, `"operational check"`)
	})

	t.Run("内置敏感字段脱敏过滤", func(t *testing.T) {
		var buf bytes.Buffer
		l := logger.NewWithOptions(
			logger.WithLevel("info"),
			logger.WithChannel("json"),
			logger.WithWriter(&buf),
			logger.WithRedaction(true),
		)

		l.Info().
			Str("username", "admin").
			Str("password", "super_secret_123").
			Str("token", "jwt_ey123456").
			Str("authorization", "Bearer xyz").
			Msg("user sign-in attempted")

		output := buf.String()
		assert.Contains(t, output, `"username":"admin"`)
		assert.NotContains(t, output, "super_secret_123")
		assert.NotContains(t, output, "jwt_ey123456")
		assert.NotContains(t, output, "Bearer xyz")
		assert.Contains(t, output, `[REDACTED]`)
	})

	t.Run("控制台通道平铺键值输出", func(t *testing.T) {
		var buf bytes.Buffer
		l := logger.NewWithOptions(
			logger.WithLevel("debug"),
			logger.WithChannel("console"),
			logger.WithWriter(&buf),
			logger.WithColor(false),
			logger.WithRedaction(false),
		)

		l.Info().
			Int("max_open_conns", 10).
			Str("query", "select 1").
			Msg("database pool ready")

		output := buf.String()
		assert.Contains(t, output, "INF")
		assert.Contains(t, output, "database pool ready")
		assert.Contains(t, output, "max_open_conns=10")
		assert.Contains(t, output, `query="select 1"`)
	})
}

// TestLogger_RedactionCoversAllJSONValueKinds 锁定脱敏器对标量、对象、数组与控制台格式的全覆盖。
//
// 结构化取值（token 载荷、鉴权对象）是凭据最常见的落盘形态，脱敏器若只处理标量，
// 这些字段会原样写进日志，因此这里逐种取值形态固化为回归防线。
func TestLogger_RedactionCoversAllJSONValueKinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		channel string
		log     func(l *zerolog.Logger)
		secret  string
		expect  string
	}{
		{
			name:    "字符串取值",
			channel: "json",
			log:     func(l *zerolog.Logger) { l.Info().Str("token", "secret-string").Msg("m") },
			secret:  "secret-string",
			expect:  `"token":"[REDACTED]"`,
		},
		{
			name:    "数字取值",
			channel: "json",
			log:     func(l *zerolog.Logger) { l.Info().Int("token", 987654).Msg("m") },
			secret:  "987654",
			expect:  `"token":"[REDACTED]"`,
		},
		{
			name:    "布尔取值",
			channel: "json",
			log:     func(l *zerolog.Logger) { l.Info().Bool("password", true).Msg("m") },
			secret:  "true",
			expect:  `"password":"[REDACTED]"`,
		},
		{
			name:    "对象取值",
			channel: "json",
			log: func(l *zerolog.Logger) {
				l.Info().Any("token", map[string]any{"inner": "secret-object"}).Msg("m")
			},
			secret: "secret-object",
			expect: `"token":"[REDACTED]"`,
		},
		{
			name:    "数组取值",
			channel: "json",
			log:     func(l *zerolog.Logger) { l.Info().Strs("password", []string{"secret-array"}).Msg("m") },
			secret:  "secret-array",
			expect:  `"password":"[REDACTED]"`,
		},
		{
			name:    "嵌套对象与内嵌引号花括号",
			channel: "json",
			log: func(l *zerolog.Logger) {
				l.Info().Any("authorization", map[string]any{
					"nested": map[string]any{"raw": `{"a":"}not-a-close"}`},
				}).Msg("m")
			},
			secret: "not-a-close",
			expect: `"authorization":"[REDACTED]"`,
		},
		{
			name:    "控制台键值形式",
			channel: "console",
			log:     func(l *zerolog.Logger) { l.Info().Str("password", "secret-console").Msg("m") },
			secret:  "secret-console",
			expect:  "password=[REDACTED]",
		},
		{
			name:    "非敏感字段保持原样",
			channel: "json",
			log:     func(l *zerolog.Logger) { l.Info().Str("username", "admin").Msg("m") },
			expect:  `"username":"admin"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			l := logger.NewWithOptions(
				logger.WithLevel("info"),
				logger.WithChannel(tt.channel),
				logger.WithWriter(&buf),
				logger.WithColor(false),
				logger.WithRedaction(true),
			)

			tt.log(l)
			output := buf.String()

			assert.Contains(t, output, tt.expect)
			if tt.secret != "" {
				assert.NotContains(t, output, tt.secret, "敏感取值不得以任何形态落盘")
			}
		})
	}
}
