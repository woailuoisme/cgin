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

	shutdown, err := telemetry.Init(context.Background(), cfg, "cgin", "test", "v1.0.0")
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	assert.False(t, telemetry.IsInitialized())

	err = shutdown(context.Background())
	require.NoError(t, err)

	tr := telemetry.Tracer("test")
	assert.NotNil(t, tr)

	meter := telemetry.Meter("test")
	assert.NotNil(t, meter)

	hook := telemetry.NewZerologHook("cgin", zerolog.InfoLevel)
	assert.Nil(t, hook)
}

func TestTelemetryConfig_Helpers(t *testing.T) {
	t.Run("EffectiveServiceName", func(t *testing.T) {
		cfg := config.TelemetryConfig{ServiceName: "custom-service"}
		assert.Equal(t, "custom-service", cfg.EffectiveServiceName("fallback"))

		emptyCfg := config.TelemetryConfig{}
		assert.Equal(t, "fallback", emptyCfg.EffectiveServiceName("fallback"))
		assert.Equal(t, "cgin", emptyCfg.EffectiveServiceName(""))
	})

	t.Run("EffectiveEndpoint", func(t *testing.T) {
		cfg := config.TelemetryConfig{Endpoint: "collector:4317"}
		assert.Equal(t, "collector:4317", cfg.EffectiveEndpoint())

		emptyCfg := config.TelemetryConfig{}
		assert.Equal(t, "http://127.0.0.1:4317", emptyCfg.EffectiveEndpoint())
	})

	t.Run("EffectiveSampleRatio", func(t *testing.T) {
		cfg := config.TelemetryConfig{SampleRatio: 0.5}
		assert.InDelta(t, 0.5, cfg.EffectiveSampleRatio(), 0.0001)

		negCfg := config.TelemetryConfig{SampleRatio: -0.1}
		assert.InDelta(t, 0.0, negCfg.EffectiveSampleRatio(), 0.0001)

		overCfg := config.TelemetryConfig{SampleRatio: 1.5}
		assert.InDelta(t, 1.0, overCfg.EffectiveSampleRatio(), 0.0001)
	})

	t.Run("MetricInterval", func(t *testing.T) {
		cfg := config.TelemetryConfig{MetricInterval: 30 * time.Second}
		assert.Equal(t, 30*time.Second, cfg.MetricInterval)
	})
}
