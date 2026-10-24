package platform_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"bgin/internal/config"
	platformsentry "bgin/internal/platform/sentry"
)

// TestSentryConfig 验证 Sentry 与 GlitchTip 别名配置优先级及启用判定。
func TestSentryConfig(t *testing.T) {
	t.Run("empty configuration is disabled", func(t *testing.T) {
		cfg := config.SentryConfig{}
		assert.False(t, cfg.IsEnabled())
		assert.Empty(t, cfg.EffectiveDSN())
	})

	t.Run("sentry DSN takes precedence", func(t *testing.T) {
		cfg := config.SentryConfig{
			DSN:          "http://key1@localhost:18080/1",
			GlitchTipDSN: "http://key2@localhost:18080/2",
		}
		assert.True(t, cfg.IsEnabled())
		assert.Equal(t, "http://key1@localhost:18080/1", cfg.EffectiveDSN())
	})

	t.Run("glitchtip DSN fallback works when sentry DSN is empty", func(t *testing.T) {
		cfg := config.SentryConfig{
			GlitchTipDSN: "http://key2@localhost:18080/2",
		}
		assert.True(t, cfg.IsEnabled())
		assert.Equal(t, "http://key2@localhost:18080/2", cfg.EffectiveDSN())
	})

	t.Run("explicitly disabled overrides DSN", func(t *testing.T) {
		disabled := false
		cfg := config.SentryConfig{
			Enabled: &disabled,
			DSN:     "http://key1@localhost:18080/1",
		}
		assert.False(t, cfg.IsEnabled())
		assert.Equal(t, "http://key1@localhost:18080/1", cfg.EffectiveDSN())
	})
}

// TestSentry_InitDisabled 验证禁用或空 DSN 初始化时为纯无操作模式。
func TestSentry_InitDisabled(t *testing.T) {
	t.Run("empty DSN is no-op", func(t *testing.T) {
		cfg := config.SentryConfig{DSN: ""}
		flush, err := platformsentry.Init(cfg, "bgin", "testing", "1.0.0")
		require.NoError(t, err)
		require.NotNil(t, flush)
		flush()
	})

	t.Run("explicitly disabled with DSN is no-op", func(t *testing.T) {
		disabled := false
		cfg := config.SentryConfig{
			Enabled: &disabled,
			DSN:     "http://invalid@localhost:18080/1",
		}
		flush, err := platformsentry.Init(cfg, "bgin", "testing", "1.0.0")
		require.NoError(t, err)
		require.NotNil(t, flush)
		flush()
	})
}

// TestSentry_InitAndCapture 验证有效配置初始化及消息、异常上报。
func TestSentry_InitAndCapture(t *testing.T) {
	cfg := config.SentryConfig{
		DSN:              "http://a2966c81939745ae99fcfdc4fe4c3f0e@localhost:18080/1",
		Environment:      "test",
		Release:          "bgin@1.0.0",
		TracesSampleRate: 0.1,
	}

	flush, err := platformsentry.Init(cfg, "bgin", "testing", "1.0.0")
	require.NoError(t, err)
	require.NotNil(t, flush)
	defer flush()

	assert.True(t, platformsentry.IsInitialized())

	// 验证消息与异常捕获
	msgID := platformsentry.CaptureMessage("Test diagnostic message")
	assert.NotNil(t, msgID)

	ctx := context.Background()
	msgCtxID := platformsentry.CaptureMessageWithContext(ctx, "Test message with context")
	assert.NotNil(t, msgCtxID)

	errID := platformsentry.CaptureException(errors.New("test error"))
	assert.NotNil(t, errID)

	errCtxID := platformsentry.CaptureExceptionWithContext(ctx, errors.New("test context error"))
	assert.NotNil(t, errCtxID)

	// 验证用户与标签设置
	platformsentry.SetUser(ctx, "user-123", "test@example.com", "tester", "127.0.0.1")
	platformsentry.SetTag(ctx, "request_id", "req-xyz-789")

	// 验证 Flush 超时等待
	platformsentry.Flush(100 * time.Millisecond)
}

// TestSentry_ZapCore 验证 Zap 结构化日志桥接。
func TestSentry_ZapCore(t *testing.T) {
	core := platformsentry.NewZapCore(zapcore.WarnLevel)
	require.NotNil(t, core)

	// 只有 Sentry 初始化后才会启用
	assert.True(t, core.Enabled(zapcore.WarnLevel))
	assert.True(t, core.Enabled(zapcore.ErrorLevel))
	assert.False(t, core.Enabled(zapcore.InfoLevel))

	logger := zap.New(core)
	require.NotNil(t, logger)

	// 记录 Warn（记录为 Sentry 面包屑）与 Error（生成 Sentry 事件）
	logger.Warn("Warning logged via Zap core", zap.String("module", "test"))
	logger.Error("Error logged via Zap core", zap.Error(errors.New("mock zap error")), zap.String("tag", "unit"))

	platformsentry.Flush(100 * time.Millisecond)
}
