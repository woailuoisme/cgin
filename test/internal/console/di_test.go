package console_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/bootstrap"
	"cgin/internal/console"
	"cgin/internal/console/core"
)

func TestDITreeOptions_Run(t *testing.T) {
	t.Parallel()

	streams, _, out, _ := core.TestIOStreams()
	injector := bootstrap.NewInjector()

	f := &console.TestFactory{
		StreamsValue:  streams,
		InjectorValue: injector,
	}

	opts := console.NewDITreeOptions(f)
	require.NoError(t, opts.Validate())

	err := opts.Run(context.Background())
	require.NoError(t, err)

	output := out.String()
	assert.Contains(t, output, "Dependency Injection Container")
	assert.Contains(t, output, "cgin/internal/config.Config")
	assert.Contains(t, output, "Total services registered:")
}
