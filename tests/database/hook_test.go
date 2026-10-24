package database_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"bgin/internal/config"
	"bgin/internal/database"
	"bgin/internal/platform/logger"
)

// setupTestLogger 构造将日志输出到内存 buffer 的上下文 logger。
func setupTestLogger(level zapcore.Level, reqID string) (context.Context, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.TimeEncoderOfLayout(time.RFC3339)
	encCfg.EncodeDuration = zapcore.StringDurationEncoder
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encCfg),
		zapcore.AddSync(buf),
		level,
	)
	l := zap.New(core)
	ctx := logger.WithContext(context.Background(), l, reqID)
	return ctx, buf
}

func TestZapQueryHook_NormalQueryDebug(t *testing.T) {
	ctx, buf := setupTestLogger(zapcore.DebugLevel, "test-req-1")

	hook := database.NewZapQueryHook(200*time.Millisecond, true)

	ctx = hook.BeforeQuery(ctx, nil)

	event := &bun.QueryEvent{
		StartTime: time.Now().Add(-10 * time.Millisecond),
		Query:     "SELECT * FROM users WHERE id = 1",
	}

	hook.AfterQuery(ctx, event)

	out := buf.String()
	assert.Contains(t, out, `"level":"debug"`)
	assert.Contains(t, out, "SELECT * FROM users WHERE id = 1")
	assert.Contains(t, out, `"request_id":"test-req-1"`)
	assert.Contains(t, out, "database query executed")
}

func TestZapQueryHook_FailedQuery(t *testing.T) {
	ctx, buf := setupTestLogger(zapcore.InfoLevel, "test-req-2")

	hook := database.NewZapQueryHook(200*time.Millisecond, false)

	event := &bun.QueryEvent{
		StartTime: time.Now().Add(-5 * time.Millisecond),
		Query:     "SELECT * FROM non_existent_table",
		Err:       errors.New("table does not exist"),
	}

	hook.AfterQuery(ctx, event)

	out := buf.String()
	assert.Contains(t, out, `"level":"error"`)
	assert.Contains(t, out, "table does not exist")
	assert.Contains(t, out, `"request_id":"test-req-2"`)
	assert.Contains(t, out, "database query failed")
}

func TestZapQueryHook_ErrNoRowsNotTreatedAsError(t *testing.T) {
	ctx, buf := setupTestLogger(zapcore.InfoLevel, "test-req-3")

	hook := database.NewZapQueryHook(200*time.Millisecond, false)

	event := &bun.QueryEvent{
		StartTime: time.Now().Add(-5 * time.Millisecond),
		Query:     "SELECT * FROM users WHERE email = 'notfound@example.com'",
		Err:       sql.ErrNoRows,
	}

	hook.AfterQuery(ctx, event)

	out := buf.String()
	assert.NotContains(t, out, `"level":"error"`)
	assert.NotContains(t, out, "database query failed")
}

func TestZapQueryHook_ContextCanceledNotTreatedAsError(t *testing.T) {
	ctx, buf := setupTestLogger(zapcore.InfoLevel, "test-req-canceled")

	hook := database.NewZapQueryHook(200*time.Millisecond, false)

	event := &bun.QueryEvent{
		StartTime: time.Now().Add(-5 * time.Millisecond),
		Query:     "SELECT * FROM users WHERE id = 1",
		Err:       context.Canceled,
	}

	hook.AfterQuery(ctx, event)

	out := buf.String()
	assert.NotContains(t, out, `"level":"error"`)
	assert.NotContains(t, out, "database query failed")
}

func TestZapQueryHook_SlowQueryWarning(t *testing.T) {
	ctx, buf := setupTestLogger(zapcore.InfoLevel, "test-req-4")

	hook := database.NewZapQueryHook(50*time.Millisecond, false)

	event := &bun.QueryEvent{
		StartTime: time.Now().Add(-100 * time.Millisecond),
		Query:     "SELECT pg_sleep(1)",
	}

	hook.AfterQuery(ctx, event)

	out := buf.String()
	assert.Contains(t, out, `"level":"warn"`)
	assert.Contains(t, out, "SELECT pg_sleep(1)")
	assert.Contains(t, out, `"threshold":"50ms"`)
	assert.Contains(t, out, "slow database query detected")
}

func TestAddQueryHooks(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	cfg := config.DatabaseConfig{
		SlowThreshold: 100 * time.Millisecond,
		Debug:         true,
	}

	assert.NotPanics(t, func() {
		db = database.AddQueryHooks(db, cfg)
	})
	assert.NotNil(t, db)

	assert.Nil(t, database.AddQueryHooks(nil, cfg))
}

func TestZapQueryHook_RedactSensitiveParameters(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Non-sensitive query untouched",
			input:    "SELECT * FROM users WHERE status = 'active' AND role = 'admin'",
			expected: "SELECT * FROM users WHERE status = 'active' AND role = 'admin'",
		},
		{
			name:     "Update password assignment redacted",
			input:    "UPDATE users SET password = 'plainPassword123', updated_at = NOW() WHERE id = 1",
			expected: "UPDATE users SET password = '[REDACTED]', updated_at = NOW() WHERE id = 1",
		},
		{
			name:     "Where token and verify_code redacted",
			input:    "SELECT * FROM verifications WHERE token = 'my-secret-token' AND verify_code = '888999'",
			expected: "SELECT * FROM verifications WHERE token = '[REDACTED]' AND verify_code = '[REDACTED]'",
		},
		{
			name:     "Argon2 hash redacted",
			input:    "INSERT INTO accounts (hash) VALUES ('$argon2id$v=19$m=65536,t=3,p=4$someSalt$someHashABC123')",
			expected: "INSERT INTO accounts (hash) VALUES ('[REDACTED]')",
		},
		{
			name:     "Bcrypt hash redacted",
			input:    "SELECT * FROM accounts WHERE hash = '$2a$12$e868u8J609d94iP55B1zMOnFpI5vj/5i8wXz1gQ7Z1F/5a/yD7VqW'",
			expected: "SELECT * FROM accounts WHERE hash = '[REDACTED]'",
		},
		{
			name:     "JWT token redacted",
			input:    "SELECT * FROM sessions WHERE refresh_token = 'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature'",
			expected: "SELECT * FROM sessions WHERE refresh_token = '[REDACTED]'",
		},
		{
			name:     "Insert into with sensitive columns redacted",
			input:    "INSERT INTO accounts (id, email, password, role) VALUES (1, 'alice@example.com', 'secretPassword', 'admin')",
			expected: "INSERT INTO accounts (id, email, password, role) VALUES (1, 'alice@example.com', '[REDACTED]', 'admin')",
		},
		{
			name:     "Insert into with null and default preserved",
			input:    "INSERT INTO accounts (email, password) VALUES ('bob@example.com', NULL), ('charlie@example.com', DEFAULT)",
			expected: "INSERT INTO accounts (email, password) VALUES ('bob@example.com', NULL), ('charlie@example.com', DEFAULT)",
		},
		{
			name:     "Update password with escaped quotes redacted",
			input:    "UPDATE users SET password = 'secret''quote', updated_at = NOW() WHERE id = 1",
			expected: "UPDATE users SET password = '[REDACTED]', updated_at = NOW() WHERE id = 1",
		},
		{
			name:     "Multi-line update with newline before value redacted",
			input:    "UPDATE users\nSET password =\n'multilineSecret'\nWHERE id = 1",
			expected: "UPDATE users\nSET password =\n'[REDACTED]'\nWHERE id = 1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := database.RedactQuery(tc.input)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestZapQueryHook_CallerAttribution(t *testing.T) {
	ctx, buf := setupTestLogger(zapcore.DebugLevel, "test-caller")
	hook := database.NewZapQueryHook(200*time.Millisecond, true)

	event := &bun.QueryEvent{
		StartTime: time.Now().Add(-10 * time.Millisecond),
		Query:     "SELECT * FROM users WHERE id = 42",
	}

	hook.AfterQuery(ctx, event)

	out := buf.String()
	assert.Contains(t, out, `"caller":"tests/database/hook_test.go:`)
}

func TestZapQueryHook_SlowQueryWithCanceled(t *testing.T) {
	ctx, buf := setupTestLogger(zapcore.InfoLevel, "test-slow-canceled")
	hook := database.NewZapQueryHook(50*time.Millisecond, false)

	event := &bun.QueryEvent{
		StartTime: time.Now().Add(-100 * time.Millisecond),
		Query:     "SELECT pg_sleep(1)",
		Err:       context.Canceled,
	}

	hook.AfterQuery(ctx, event)

	out := buf.String()
	assert.Contains(t, out, `"level":"warn"`)
	assert.Contains(t, out, `"canceled":true`)
	assert.Contains(t, out, "slow database query detected")
}
