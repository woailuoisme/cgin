package console_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/console"
	"bgin/internal/console/core"
)

// TestSentryTestOptions_Disabled 验证当 Sentry/GlitchTip 未配置时输出警告提示并安全退出。
func TestSentryTestOptions_Disabled(t *testing.T) {
	streams, _, out, _ := core.TestIOStreams()
	f := &console.TestFactory{
		StreamsValue: streams,
		ConfigValue: &config.Config{
			Sentry: config.SentryConfig{
				DSN: "",
			},
		},
	}

	opts := console.NewSentryTestOptions(f)
	require.NoError(t, opts.Complete(nil, nil))
	require.NoError(t, opts.Validate())

	err := opts.Run(context.Background())
	require.NoError(t, err)

	outStr := out.String()
	assert.Contains(t, outStr, "Sentry / GlitchTip is not enabled")
	assert.Contains(t, outStr, "SENTRY_DSN or GLITCHTIP_DSN")
}

// TestSentryTestOptions_CompleteAndValidate 验证参数补全与默认消息设置。
func TestSentryTestOptions_CompleteAndValidate(t *testing.T) {
	f := &console.TestFactory{}

	t.Run("default message is preserved when empty", func(t *testing.T) {
		opts := console.NewSentryTestOptions(f)
		opts.Message = ""
		require.NoError(t, opts.Complete(nil, nil))
		assert.Equal(t, "Test GlitchTip connection from bgin CLI", opts.Message)
		require.NoError(t, opts.Validate())
	})

	t.Run("custom message is preserved", func(t *testing.T) {
		opts := console.NewSentryTestOptions(f)
		opts.Message = "Custom diagnostic ping"
		require.NoError(t, opts.Complete(nil, nil))
		assert.Equal(t, "Custom diagnostic ping", opts.Message)
		require.NoError(t, opts.Validate())
	})
}

// TestSentryTestOptions_Enabled 验证在配置了有效 DSN 时正常执行捕获并输出成功反馈。
func TestSentryTestOptions_Enabled(t *testing.T) {
	streams, _, out, _ := core.TestIOStreams()
	f := &console.TestFactory{
		StreamsValue: streams,
		ConfigValue: &config.Config{
			App: config.AppConfig{
				Name: "bgin-test",
				Env:  "testing",
			},
			Sentry: config.SentryConfig{
				DSN:              "http://a2966c81939745ae99fcfdc4fe4c3f0e@localhost:18080/1",
				TracesSampleRate: 0.01,
			},
		},
	}

	opts := console.NewSentryTestOptions(f)
	opts.Message = "Test message from unit test"
	require.NoError(t, opts.Complete(nil, nil))
	require.NoError(t, opts.Validate())

	err := opts.Run(context.Background())
	require.NoError(t, err)

	outStr := out.String()
	assert.Contains(t, outStr, "Sending test message to Sentry/GlitchTip")
	assert.Contains(t, outStr, "Test event captured and flushed successfully!")
}
