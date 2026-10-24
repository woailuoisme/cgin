// 本文件验证 Load 在启动期的拦截能力：非法 APP_ENV、缺失的必填项，以及写坏的 .env，
// 都必须当场失败，而不是让进程带着默认值或半生效的配置跑起来。
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
)

// setRequiredEnv 提供三个必填项，使断言只围绕被测的那一条失败原因展开。
// 它们走的是进程环境，因此真实 shell 里的取值都会被盖掉。
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DB_URL", "postgres://app:secret@127.0.0.1:5432/bgin?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:6379/0")
	t.Setenv("JWT_SECRET", strings.Repeat("s", 32))
}

// unsetEnv 真正摘掉这些变量，用例结束后还原。
//
// required 标签问的是"变量有没有设置"，把值设成空串并不等价，因此这里必须 Unsetenv。
// 还原只能用 os.Setenv：t.Setenv 是"立即设置 + 用例结束还原"，而这里需要的正是相反的顺序。
func unsetEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		previous, existed := os.LookupEnv(name)
		require.NoError(t, os.Unsetenv(name))
		if !existed {
			continue
		}
		t.Cleanup(func() {
			//nolint:usetesting // t.Setenv 会在调用当下就设置，等于立刻抵消上面的 Unsetenv
			_ = os.Setenv(name, previous)
		})
	}
}

// TestLoadRejectsUnknownAppEnv 锁定 APP_ENV 的取值白名单。
//
// 除拼写错误外，这里特意包含大小写写错的取值——它同样会被静默当成"非本地、非生产"，
// 让本地的调试便利与生产的收敛性同时失效。
func TestLoadRejectsUnknownAppEnv(t *testing.T) {
	for _, env := range []string{"Producton", "PRODUCTION", "prod", "dev", "local/../production"} {
		t.Run(env, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("APP_ENV", env)

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "APP_ENV")
		})
	}
}

// TestLoadAcceptsKnownAppEnvs 是上一条的对照组。
func TestLoadAcceptsKnownAppEnvs(t *testing.T) {
	for _, env := range []string{"local", "development", "test", "staging", "production"} {
		t.Run(env, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("APP_ENV", env)
			if env == "production" {
				// 生产环境禁止通配来源，本地 .env 里的 * 会在此被拦下，
				// 因此该用例必须显式给出真实来源（见 TestLoadRejectsWildcardCORSInProduction）。
				t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")
				// 生产环境同样不接受日志/假短信驱动（会把验证码写进日志），必须指向真实服务商。
				t.Setenv("NOTIFICATION_SMS_DRIVER", "aliyun")
				t.Setenv("NOTIFICATION_SMS_ALIYUN_ACCESS_KEY_ID", "test-ak")
				t.Setenv("NOTIFICATION_SMS_ALIYUN_ACCESS_KEY_SECRET", "test-sk")
			}

			cfg, err := config.Load()
			require.NoError(t, err)
			assert.Equal(t, env, cfg.App.Env)
		})
	}
}

// TestLoadRejectsDebugSMSDriverInProduction 锁定生产环境不得使用日志/内存假短信驱动。
//
// 这两类驱动在开发期很方便，但在生产会把手机号与验证码明文写进日志（log），
// 或让验证码根本不发出去却回执成功（mock）——两者都必须当场拦下。
func TestLoadRejectsDebugSMSDriverInProduction(t *testing.T) {
	for _, driver := range []string{"log", "mock", "LOG"} {
		t.Run(driver, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("APP_ENV", "production")
			t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")
			t.Setenv("NOTIFICATION_SMS_DRIVER", driver)

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "notification_sms_driver")
		})
	}

	// 留空时归一为 none：不注册任何驱动，既不泄漏也不阻塞启动。
	t.Run("留空时归一为 none", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("APP_ENV", "production")
		t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")
		t.Setenv("NOTIFICATION_SMS_DRIVER", "")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, "none", cfg.Notification.SMS.Driver)
	})
}

// TestLoadRejectsWildcardCORSInProduction 锁定生产环境禁止通配跨域来源。
// 通配意味着任意站点都能调用该 API，且它不会报错、只是安静地放行，必须在启动期拦下。
func TestLoadRejectsWildcardCORSInProduction(t *testing.T) {
	t.Run("生产环境拒绝通配", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("APP_ENV", "production")
		t.Setenv("CORS_ALLOWED_ORIGINS", "*")

		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "CORS_ALLOWED_ORIGINS")
	})

	t.Run("非生产环境允许通配", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("APP_ENV", "local")
		t.Setenv("CORS_ALLOWED_ORIGINS", "*")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, []string{"*"}, cfg.CORS.AllowedOrigins)
	})
}

// TestLoadRejectsMalformedCORSOrigins 锁定来源白名单的格式校验：
// 多写一个逗号或漏写 scheme 都会静默地不让任何来源通过，排障时极难定位。
func TestLoadRejectsMalformedCORSOrigins(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "多余逗号", raw: "https://a.com,"},
		{name: "缺少 scheme", raw: "a.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("APP_ENV", "local")
			t.Setenv("CORS_ALLOWED_ORIGINS", tt.raw)

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "CORS_ALLOWED_ORIGINS")
		})
	}
}

// TestLoadRequiresConnectionSettings 锁定字段上的 required 标签：三个连接与签名相关的
// 必填项缺一不可，且报错要点名是哪一个，否则运维只能靠猜。
func TestLoadRequiresConnectionSettings(t *testing.T) {
	for _, name := range []string{"DB_URL", "REDIS_URL", "JWT_SECRET"} {
		t.Run(name, func(t *testing.T) {
			setRequiredEnv(t)
			unsetEnv(t, name)

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), name)
		})
	}
}

// TestLoadRejectsEmptyRequiredEnv 锁定 env 标签里的 notEmpty 选项：把值设成空串不算
// "提供了"。少了它，空串会一路走到解析 DSN 或签名令牌时才炸，报错点离原因很远。
func TestLoadRejectsEmptyRequiredEnv(t *testing.T) {
	for _, name := range []string{"DB_URL", "REDIS_URL", "JWT_SECRET"} {
		t.Run(name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(name, "")

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), name)
		})
	}
}

// TestLoadReportsMalformedDotEnv 锁定"文件写坏必须报错"。
//
// 被吞掉的格式错误会表现为"一批必填项突然缺失"，让人往环境没配好的方向排查，
// 而真正的原因是文件本身。
func TestLoadReportsMalformedDotEnv(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".env"),
		[]byte("MAIL_FROM_NAME=\"unterminated\n"),
		0o600,
	))
	t.Chdir(dir)
	setRequiredEnv(t)

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".env", "报错应当指向文件本身")
}
