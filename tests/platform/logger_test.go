package platform_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/logger"
)

// TestLogger_BuildAndContext 验证日志器构建与 Context 请求标识传递。
func TestLogger_BuildAndContext(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{
			Name: "bgin-test",
			Env:  "local",
		},
		Log: config.LogConfig{
			Level:   "debug",
			Channel: "console",
		},
	}

	l := logger.New(cfg)
	require.NotNil(t, l)

	ctx := logger.WithContext(context.Background(), l, "req_test_123")
	extracted, ok := logger.FromContextOK(ctx)
	assert.True(t, ok)
	assert.NotNil(t, extracted)

	// 缺失时返回默认兜底日志器
	fallback := logger.FromContext(context.Background())
	assert.NotNil(t, fallback)

	sugar := logger.SugarFromContext(ctx)
	assert.NotNil(t, sugar)
}

// TestLogger_ChannelJSON 验证 JSON 通道构建。
func TestLogger_ChannelJSON(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{
			Name: "bgin-test",
			Env:  "production",
		},
		Log: config.LogConfig{
			Level:   "info",
			Channel: "json",
		},
	}

	l := logger.New(cfg)
	require.NotNil(t, l)
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
		assert.NotNil(t, l)
	})

	t.Run("FORCE_COLOR 等于 0 或 false 时显式禁用着色", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("FORCE_COLOR", "0")

		cfg := &config.Config{
			App: config.AppConfig{Env: "production"},
			Log: config.LogConfig{Channel: "console"},
		}
		l := logger.New(cfg)
		assert.NotNil(t, l)

		t.Setenv("FORCE_COLOR", "false")
		l2 := logger.New(cfg)
		assert.NotNil(t, l2)
	})

	t.Run("FORCE_COLOR 或 CLICOLOR_FORCE 强制着色", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("FORCE_COLOR", "1")

		cfg := &config.Config{
			App: config.AppConfig{Env: "production"},
			Log: config.LogConfig{Channel: "console"},
		}
		l := logger.New(cfg)
		assert.NotNil(t, l)

		t.Setenv("FORCE_COLOR", "")
		t.Setenv("CLICOLOR_FORCE", "1")
		l2 := logger.New(cfg)
		assert.NotNil(t, l2)
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
		require.NotNil(t, l)

		l.Info("operational check", zap.String("action", "test_run"))
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
		require.NotNil(t, l)

		l.Info("user sign-in attempted",
			zap.String("username", "admin"),
			zap.String("password", "super_secret_123"),
			zap.String("token", "jwt_ey123456"),
			zap.String("authorization", "Bearer xyz"),
		)

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
		require.NotNil(t, l)

		l.Info("database pool ready",
			zap.Int("max_open_conns", 10),
			zap.String("query", "select 1"),
		)

		output := buf.String()
		assert.Contains(t, output, "INFO")
		assert.Contains(t, output, "database pool ready")
		assert.Contains(t, output, "max_open_conns=10")
		assert.Contains(t, output, "query=\"select 1\"")
		assert.NotContains(t, output, `{"max_open_conns": 10`)
	})
}
