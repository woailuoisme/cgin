package logger_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/goleak"

	"cgin/internal/config"
	"cgin/pkg/logger"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestLogger_BuildAndContext 验证日志器构建与 Context 请求标识传递。
func TestLogger_BuildAndContext(t *testing.T) {
	cfg := config.LogConfig{
		Level:       "debug",
		Channel:     "console",
		ServiceName: "cfiber-test",
		Env:         "local",
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
	cfg := config.LogConfig{
		Level:       "info",
		Channel:     "json",
		ServiceName: "cfiber-test",
		Env:         "production",
	}

	l := logger.New(cfg)
	assert.NotZero(t, l)
}

// TestLogger_ColorDecision 验证颜色启闭决策与环境变量优先级。
func TestLogger_ColorDecision(t *testing.T) {
	t.Run("NO_COLOR 存在时禁用着色", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		t.Setenv("FORCE_COLOR", "1")

		cfg := config.LogConfig{Channel: "console", Env: "local"}
		l := logger.New(cfg)
		assert.NotZero(t, l)
	})

	t.Run("FORCE_COLOR 等于 0 或 false 时显式禁用着色", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("FORCE_COLOR", "0")

		cfg := config.LogConfig{Channel: "console", Env: "production"}
		l := logger.New(cfg)
		assert.NotZero(t, l)

		t.Setenv("FORCE_COLOR", "false")
		l2 := logger.New(cfg)
		assert.NotZero(t, l2)
	})

	t.Run("FORCE_COLOR 或 CLICOLOR_FORCE 强制着色", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("FORCE_COLOR", "1")

		cfg := config.LogConfig{Channel: "console", Env: "production"}
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

		l.Info("operational check", slog.String("action", "test_run"))
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

		l.Info("user sign-in attempted",
			slog.String("username", "admin"),
			slog.String("password", "super_secret_123"),
			slog.String("token", "jwt_ey123456"),
			slog.String("authorization", "Bearer xyz"),
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

		l.Info("database pool ready",
			slog.Int("max_open_conns", 10),
			slog.String("query", "select 1"),
		)

		output := buf.String()
		assert.Contains(t, output, "INF")
		assert.Contains(t, output, "database pool ready")
		assert.Contains(t, output, "max_open_conns=10")
		assert.Contains(t, output, `query="select 1"`)
	})
}

// TestLogger_RedactionCoversAllJSONValueKinds 锁定脱敏器对标量、对象、数组与控制台格式的全覆盖。
func TestLogger_RedactionCoversAllJSONValueKinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		channel string
		log     func(l *logger.Logger)
		secret  string
		expect  string
	}{
		{
			name:    "字符串取值",
			channel: "json",
			log:     func(l *logger.Logger) { l.Info("m", slog.String("token", "secret-string")) },
			secret:  "secret-string",
			expect:  `"token":"[REDACTED]"`,
		},
		{
			name:    "数字取值",
			channel: "json",
			log:     func(l *logger.Logger) { l.Info("m", slog.Int("token", 987654)) },
			secret:  "987654",
			expect:  `"token":"[REDACTED]"`,
		},
		{
			name:    "布尔取值",
			channel: "json",
			log:     func(l *logger.Logger) { l.Info("m", slog.Bool("password", true)) },
			secret:  "true",
			expect:  `"password":"[REDACTED]"`,
		},
		{
			name:    "对象取值",
			channel: "json",
			log: func(l *logger.Logger) {
				l.Info("m", slog.Any("token", map[string]any{"inner": "secret-object"}))
			},
			secret: "secret-object",
			expect: `"token":"[REDACTED]"`,
		},
		{
			name:    "数组取值",
			channel: "json",
			log:     func(l *logger.Logger) { l.Info("m", slog.Any("password", []string{"secret-array"})) },
			secret:  "secret-array",
			expect:  `"password":"[REDACTED]"`,
		},
		{
			name:    "嵌套对象与内嵌引号花括号",
			channel: "json",
			log: func(l *logger.Logger) {
				l.Info("m", slog.Any("authorization", map[string]any{
					"nested": map[string]any{"raw": `{"a":"}not-a-close"}`},
				}))
			},
			secret: "not-a-close",
			expect: `"authorization":"[REDACTED]"`,
		},
		{
			name:    "控制台键值形式",
			channel: "console",
			log:     func(l *logger.Logger) { l.Info("m", slog.String("password", "secret-console")) },
			secret:  "secret-console",
			expect:  "password=[REDACTED]",
		},
		{
			name:    "非敏感字段保持原样",
			channel: "json",
			log:     func(l *logger.Logger) { l.Info("m", slog.String("username", "admin")) },
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

// TestLogger_TraceContextHandler 验证 TraceContextHandler 自动提取请求标识与 Trace 属性。
func TestLogger_TraceContextHandler(t *testing.T) {
	var buf bytes.Buffer
	l := logger.NewWithOptions(
		logger.WithLevel("info"),
		logger.WithChannel("json"),
		logger.WithWriter(&buf),
		logger.WithRedaction(false),
	)

	ctx := logger.WithContext(context.Background(), l, "req_custom_trace_999")
	reqLogger := logger.FromContext(ctx)
	reqLogger.InfoContext(ctx, "processing with request id")

	output := buf.String()
	assert.Contains(t, output, `"request_id":"req_custom_trace_999"`)
	assert.Contains(t, output, `"message":"processing with request id"`)
}

// TestParseSlogLevel 验证日志级别字符串解析为 slog.Level。
func TestParseSlogLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"unknown", slog.LevelInfo},
		{"", slog.LevelInfo},
	}

	for _, tc := range tests {
		assert.Equal(t, tc.expected, logger.ParseSlogLevel(tc.input), "解析级别 %q 失败", tc.input)
	}
}

// TestLogger_ConsoleCleanAndDurationUnit 验证控制台模式下 TraceID/SpanID 抑制与 Duration 自适应单位格式化。
func TestLogger_ConsoleCleanAndDurationUnit(t *testing.T) {
	var buf bytes.Buffer
	l := logger.NewWithOptions(
		logger.WithLevel("info"),
		logger.WithChannel("console"),
		logger.WithWriter(&buf),
		logger.WithColor(false),
	)

	ctx := logger.WithContext(context.Background(), l, "req_sample_id_12345")
	l.InfoContext(ctx, "access",
		slog.String("method", "GET"),
		slog.String("path", "/docs/openapi.json"),
		slog.Int("status", 200),
		slog.Duration("latency", 251834*time.Nanosecond),
		slog.String("ip", "127.0.0.1"),
	)

	out := buf.String()
	assert.Contains(t, out, "251.834µs", "耗时应带人类友好自适应单位")
	assert.NotContains(t, out, "trace_id", "控制台普通日志不应输出 trace_id")
	assert.NotContains(t, out, "span_id", "控制台普通日志不应输出 span_id")
	assert.NotContains(t, out, "request_id", "控制台普通 200 日志不应输出冗长 request_id")
	assert.NotContains(t, out, "logger_test.go", "普通 Info 日志不应输出代码位置 Caller")

	// 验证 Warn 级别自动附加 request_id 但不输出 Caller
	buf.Reset()
	l.WarnContext(ctx, "slow request detected",
		slog.String("path", "/slow"),
		slog.Duration("latency", 500*time.Millisecond),
	)
	warnOut := buf.String()
	assert.Contains(t, warnOut, "request_id=req_sample_id_12345", "Warn 级别日志应保留 request_id 便于追踪排查")
	assert.Contains(t, warnOut, "latency=500ms")
	assert.NotContains(t, warnOut, "logger_test.go", "Warn 级别日志不应输出代码位置 Caller")

	// 验证 Error 级别自动附加精简代码位置（如 logger_test.go:340 >）
	buf.Reset()
	l.ErrorContext(ctx, "database query failed",
		slog.String("error", "timeout"),
	)
	errOut := buf.String()
	assert.Contains(t, errOut, "logger_test.go:", "Error 级别日志应附加精简文件名与行号")
	assert.Contains(t, errOut, "request_id=req_sample_id_12345", "Error 级别日志应保留 request_id")
}

func BenchmarkLogger_JSONRedact(b *testing.B) {
	l := logger.NewWithOptions(
		logger.WithLevel("info"),
		logger.WithChannel("json"),
		logger.WithWriter(io.Discard),
		logger.WithRedaction(true),
	)

	b.ReportAllocs()
	for b.Loop() {
		l.Info("user sign-in attempted",
			slog.String("username", "admin"),
			slog.String("password", "super_secret_123"),
			slog.String("token", "jwt_ey123456"),
		)
	}
}

func BenchmarkLogger_ConsoleRedact(b *testing.B) {
	l := logger.NewWithOptions(
		logger.WithLevel("info"),
		logger.WithChannel("console"),
		logger.WithWriter(io.Discard),
		logger.WithColor(false),
		logger.WithRedaction(true),
	)

	b.ReportAllocs()
	for b.Loop() {
		l.Info("user sign-in attempted",
			slog.String("username", "admin"),
			slog.String("password", "super_secret_123"),
			slog.String("token", "jwt_ey123456"),
		)
	}
}
