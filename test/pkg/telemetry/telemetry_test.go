package telemetry_test

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/pkg/telemetry"
)

func TestTelemetry_Disabled(t *testing.T) {
	cfg := config.TelemetryConfig{
		Enabled: false,
	}

	tp, err := telemetry.New(context.Background(), cfg, "test", "v1.0.0")
	require.NoError(t, err)
	require.NotNil(t, tp)
	assert.False(t, tp.Enabled())
	assert.False(t, telemetry.IsInitialized())

	hook := tp.ZerologHook("cgin", zerolog.InfoLevel)
	assert.Nil(t, hook)

	err = tp.Shutdown(context.Background())
	require.NoError(t, err)

	tr := telemetry.Tracer("test")
	assert.NotNil(t, tr)

	meter := telemetry.Meter("test")
	assert.NotNil(t, meter)
}

func TestTelemetryConfig_NormalizeAndValidate(t *testing.T) {
	t.Run("NormalizeServiceName", func(t *testing.T) {
		cfg := config.TelemetryConfig{ServiceName: "custom-service"}
		cfg.Normalize("fallback")
		assert.Equal(t, "custom-service", cfg.ServiceName)

		emptyCfg := config.TelemetryConfig{}
		emptyCfg.Normalize("fallback")
		assert.Equal(t, "fallback", emptyCfg.ServiceName)

		defaultCfg := config.TelemetryConfig{}
		defaultCfg.Normalize("")
		assert.Equal(t, "cgin", defaultCfg.ServiceName)
	})

	t.Run("NormalizeEndpoint", func(t *testing.T) {
		cfg := config.TelemetryConfig{Endpoint: "collector:4317"}
		cfg.Normalize("")
		assert.Equal(t, "collector:4317", cfg.Endpoint)

		emptyCfg := config.TelemetryConfig{}
		emptyCfg.Normalize("")
		assert.Equal(t, "http://127.0.0.1:4317", emptyCfg.Endpoint)
	})

	t.Run("ValidateSampleRatio", func(t *testing.T) {
		cfg := config.TelemetryConfig{Enabled: true, SampleRatio: 0.5}
		require.NoError(t, cfg.Validate())

		negCfg := config.TelemetryConfig{Enabled: true, SampleRatio: -0.1}
		require.Error(t, negCfg.Validate())

		overCfg := config.TelemetryConfig{Enabled: true, SampleRatio: 1.5}
		require.Error(t, overCfg.Validate())
	})

	t.Run("MetricInterval", func(t *testing.T) {
		cfg := config.TelemetryConfig{MetricInterval: 30 * time.Second}
		cfg.Normalize("")
		assert.Equal(t, 30*time.Second, cfg.MetricInterval)
	})
}
