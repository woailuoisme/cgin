package bootstrap_test

import (
	"context"
	"testing"

	"github.com/samber/do/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/bootstrap"
	"cgin/internal/config"
	"cgin/pkg/geoip"
	"cgin/pkg/mailer"
)

func TestNewInjector_RegistrationAndClone(t *testing.T) {
	t.Setenv("DB_URL", "postgres://app:secret@127.0.0.1:5432/cgin?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:6379/0")
	t.Setenv("JWT_SECRET", "12345678901234567890123456789012")
	t.Setenv("SESSION_SECRET", "12345678901234567890123456789012")

	injector := bootstrap.NewInjector()
	require.NotNil(t, injector)

	provided := injector.ListProvidedServices()
	assert.NotEmpty(t, provided)

	// 测试容器克隆 (Clone) 与依赖覆盖 (Override)
	clone := injector.Clone()
	require.NotNil(t, clone)

	fakeCfg := &config.Config{}
	fakeCfg.App.Name = "cloned-app-test"
	do.OverrideValue(clone, fakeCfg)

	cfg, err := do.Invoke[*config.Config](clone)
	require.NoError(t, err)
	assert.Equal(t, "cloned-app-test", cfg.App.Name)

	// 原容器不受影响
	rootCfg, err := do.Invoke[*config.Config](injector)
	require.NoError(t, err)
	assert.NotEqual(t, "cloned-app-test", rootCfg.App.Name)
}

type stubShutdownMailer struct{}

func (s *stubShutdownMailer) Send(context.Context, mailer.Message) error { return nil }

func (s *stubShutdownMailer) SendMailable(context.Context, string, mailer.Mailable) error { return nil }

func (s *stubShutdownMailer) Close() error { return nil }

func (s *stubShutdownMailer) Shutdown() error { return nil }

func (s *stubShutdownMailer) Ping(context.Context) error { return nil }

func TestApplication_Shutdown(t *testing.T) {
	t.Parallel()

	injector := bootstrap.NewInjector()
	clone := injector.Clone()

	// 注册模拟测试 Mailer 验证 Shutdowner 机制
	mockMailer := &stubShutdownMailer{}
	do.OverrideValue[mailer.Mailer](clone, mockMailer)

	m, err := do.Invoke[mailer.Mailer](clone)
	require.NoError(t, err)
	assert.NotNil(t, m)

	// 测试优雅停机
	report := clone.ShutdownWithContext(context.Background())
	require.NotNil(t, report)
	assert.Empty(t, report.Errors)
}

func TestNewInjector_GeoIPService(t *testing.T) {
	t.Setenv("DB_URL", "postgres://app:secret@127.0.0.1:5432/cgin?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:6379/0")
	t.Setenv("JWT_SECRET", "12345678901234567890123456789012")
	t.Setenv("SESSION_SECRET", "12345678901234567890123456789012")

	injector := bootstrap.NewInjector()
	svc, err := do.Invoke[geoip.Service](injector)
	require.NoError(t, err)
	assert.NotNil(t, svc)

	// 验证降级与基本功能正常工作
	assert.Equal(t, "Internal IP", svc.Summary(context.Background(), "127.0.0.1"))
}
