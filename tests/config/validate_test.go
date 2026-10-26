// Package config_test 验证配置校验的失败聚合与错误信息可运维性。
package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
)

// setBaseValidEnv 注入一组可通过校验的最小环境变量，供各用例按需覆盖单项。
func setBaseValidEnv(t *testing.T) {
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

// TestLoadReportsAllValidationFailures 验证校验不短路：多个模块同时非法时一次报全。
func TestLoadReportsAllValidationFailures(t *testing.T) {
	setBaseValidEnv(t)
	t.Setenv("DB_MAX_OPEN_CONNS", "0")
	t.Setenv("OTP_TTL", "0s")
	t.Setenv("RATELIMIT_SENSITIVE", "-1")

	_, err := config.Load()
	require.Error(t, err)
	t.Logf("聚合错误：\n%s", err)

	message := err.Error()
	for _, envName := range []string{"DB_MAX_OPEN_CONNS", "OTP_TTL", "RATELIMIT_SENSITIVE"} {
		assert.Contains(t, message, envName, "聚合错误应同时包含 %s", envName)
	}
}

// TestValidationErrorsNameEnvironmentVariable 守护错误信息可运维性：
// 每条语义校验失败都必须点明对应的环境变量名，而不是只报字段名。
func TestValidationErrorsNameEnvironmentVariable(t *testing.T) {
	testCases := []struct {
		name    string
		env     [][2]string
		wantEnv string
	}{
		{
			name:    "DB_MIN_CONNS 超过 DB_MAX_OPEN_CONNS",
			env:     [][2]string{{"DB_MAX_OPEN_CONNS", "5"}, {"DB_MIN_CONNS", "10"}},
			wantEnv: "DB_MIN_CONNS",
		},
		{
			name:    "DB_CONNECT_TIMEOUT 非正",
			env:     [][2]string{{"DB_CONNECT_TIMEOUT", "0s"}},
			wantEnv: "DB_CONNECT_TIMEOUT",
		},
		{
			name:    "DB_STATEMENT_TIMEOUT 小于 1ms",
			env:     [][2]string{{"DB_STATEMENT_TIMEOUT", "1ns"}},
			wantEnv: "DB_STATEMENT_TIMEOUT",
		},
		{
			name:    "HTTP_REQUEST_TIMEOUT 超过 HTTP_WRITE_TIMEOUT",
			env:     [][2]string{{"HTTP_REQUEST_TIMEOUT", "20s"}, {"HTTP_WRITE_TIMEOUT", "15s"}},
			wantEnv: "HTTP_REQUEST_TIMEOUT",
		},
		{
			name:    "OTP_MAX_ATTEMPTS 非正",
			env:     [][2]string{{"OTP_MAX_ATTEMPTS", "0"}},
			wantEnv: "OTP_MAX_ATTEMPTS",
		},
		{
			name:    "PASSWORD_RESET_TTL 非正",
			env:     [][2]string{{"PASSWORD_RESET_TTL", "0s"}},
			wantEnv: "PASSWORD_RESET_TTL",
		},
		{
			name:    "RATELIMIT_ANONYMOUS 为负",
			env:     [][2]string{{"RATELIMIT_ANONYMOUS", "-5"}},
			wantEnv: "RATELIMIT_ANONYMOUS",
		},
		{
			name:    "CORS 生产环境通配符",
			env:     [][2]string{{"APP_ENV", "production"}, {"CORS_ALLOWED_ORIGINS", "*"}},
			wantEnv: "CORS_ALLOWED_ORIGINS",
		},
		{
			name: "S3 缺失 AWS_BUCKET",
			env: [][2]string{
				{"FILESYSTEM_DISK", "s3"},
				{"AWS_ENDPOINT", "https://s3.example.com"},
				// 本地 .env 已配置真实 S3 凭据，此处显式清空以隔离用例。
				{"AWS_BUCKET", ""},
			},
			wantEnv: "AWS_BUCKET",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			setBaseValidEnv(t)
			for _, pair := range testCase.env {
				t.Setenv(pair[0], pair[1])
			}

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantEnv)
		})
	}
}
