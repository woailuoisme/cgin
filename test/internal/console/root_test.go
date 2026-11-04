package console_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/samber/do/v2"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/internal/console"
	"cgin/internal/database"
	"cgin/pkg/logger"
)

// mockPrompter 供纯内存单元测试使用的 Prompter Mock 实现。
type mockPrompter struct {
	ConfirmResponses []bool
	InputResponses   []string
	SelectResponses  []string

	confirmIdx int
	inputIdx   int
	selectIdx  int
}

func newMockPrompter() *mockPrompter { return &mockPrompter{} }

func (m *mockPrompter) Confirm(_, _ string, _ ...string) (bool, error) {
	if m.confirmIdx < len(m.ConfirmResponses) {
		res := m.ConfirmResponses[m.confirmIdx]
		m.confirmIdx++
		return res, nil
	}
	return false, nil
}

func (m *mockPrompter) Input(_, _, defaultValue string) (string, error) {
	if m.inputIdx < len(m.InputResponses) {
		res := m.InputResponses[m.inputIdx]
		m.inputIdx++
		return res, nil
	}
	return defaultValue, nil
}

func (m *mockPrompter) Select(_, _ string, _ []string, defaultOption string) (string, error) {
	if m.selectIdx < len(m.SelectResponses) {
		res := m.SelectResponses[m.selectIdx]
		m.selectIdx++
		return res, nil
	}
	return defaultOption, nil
}

// testFactory 提供供纯内存单元测试使用的可定制工厂实现。
type testFactory struct {
	StreamsValue  *console.IOStreams
	PrompterValue console.Prompter
	ConfigValue   *config.Config
	ConfigErr     error
	DBValue       *database.DB
	DBErr         error
	LoggerValue   *logger.Logger
	InjectorValue *do.RootScope
}

func (f *testFactory) IOStreams() *console.IOStreams {
	if f.StreamsValue == nil {
		s, _, _, _ := console.TestIOStreams()
		f.StreamsValue = s
	}
	return f.StreamsValue
}

func (f *testFactory) Prompter() console.Prompter {
	if f.PrompterValue == nil {
		f.PrompterValue = newMockPrompter()
	}
	return f.PrompterValue
}

func (f *testFactory) Config() (*config.Config, error) {
	return f.ConfigValue, f.ConfigErr
}

func (f *testFactory) DB(_ context.Context) (*database.DB, error) {
	return f.DBValue, f.DBErr
}

func (f *testFactory) Logger() *logger.Logger {
	if f.LoggerValue == nil {
		return logger.Nop()
	}
	return f.LoggerValue
}

func (f *testFactory) Injector() *do.RootScope {
	if f.InjectorValue != nil {
		return f.InjectorValue
	}
	return do.New()
}

func execWithTestFactory(ctx context.Context, f *testFactory, args ...string) (string, error) {
	if f == nil {
		f = &testFactory{}
	}
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	if f.StreamsValue == nil {
		f.StreamsValue = &console.IOStreams{Out: outBuf, ErrOut: errBuf}
	}
	cmd := console.NewRootCommandWithFactory(f)
	cmd.SetOut(f.StreamsValue.Out)
	cmd.SetErr(f.StreamsValue.ErrOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	if b, ok := f.StreamsValue.Out.(*bytes.Buffer); ok {
		return b.String(), err
	}
	return outBuf.String(), err
}

func TestRootCommand_Registration(t *testing.T) {
	cmd := console.NewRootCommand()
	require.NotNil(t, cmd)

	expectedCommands := []string{
		"app:init",
		"serve",
		"db:migrate",
		"db:seed",
		"db:fresh",
		"geoip:update",
		"jwt:secret",
		"make:crud",
		"queue:work",
		"queue:failed",
		"queue:retry",
		"queue:flush",
		"route:list",
		"scout:import",
		"scout:flush",
		"di:tree",
	}

	commandNames := lo.Map(cmd.Commands(), func(c *cobra.Command, _ int) string {
		return c.Name()
	})
	for _, expected := range expectedCommands {
		assert.Contains(t, commandNames, expected)
	}

	deprecatedTopCommands := []string{
		"queue", "route", "db", "geoip", "scout", "jwt", "make", "make:destroy",
	}
	for _, deprecated := range deprecatedTopCommands {
		assert.NotContains(t, commandNames, deprecated)
	}

	groupIDs := lo.Map(cmd.Groups(), func(g *cobra.Group, _ int) string {
		return g.ID
	})
	assert.ElementsMatch(t, []string{"app", "db", "di", "geoip", "jwt", "make", "queue", "route", "scout"}, groupIDs)

	cmdMap := lo.KeyBy(cmd.Commands(), func(c *cobra.Command) string {
		return c.Name()
	})
	assert.Empty(t, cmdMap["serve"].GroupID)
	assert.Equal(t, "app", cmdMap["app:init"].GroupID)
	assert.Equal(t, "db", cmdMap["db:migrate"].GroupID)
	assert.Equal(t, "db", cmdMap["db:seed"].GroupID)
	assert.Equal(t, "db", cmdMap["db:fresh"].GroupID)
	assert.Equal(t, "di", cmdMap["di:tree"].GroupID)
	assert.Equal(t, "geoip", cmdMap["geoip:update"].GroupID)
	assert.Equal(t, "jwt", cmdMap["jwt:secret"].GroupID)
	assert.Equal(t, "make", cmdMap["make:crud"].GroupID)
	assert.Equal(t, "queue", cmdMap["queue:work"].GroupID)
	assert.Equal(t, "queue", cmdMap["queue:failed"].GroupID)
	assert.Equal(t, "queue", cmdMap["queue:retry"].GroupID)
	assert.Equal(t, "queue", cmdMap["queue:flush"].GroupID)
	assert.Equal(t, "route", cmdMap["route:list"].GroupID)
	assert.Equal(t, "scout", cmdMap["scout:import"].GroupID)
	assert.Equal(t, "scout", cmdMap["scout:flush"].GroupID)
}

func TestParseQueueWeights(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]int
	}{
		{"empty input defaults to default:1", "", map[string]int{"default": 1}},
		{"single queue name without weight", "critical", map[string]int{"critical": 1}},
		{"single queue with weight", "critical:5", map[string]int{"critical": 5}},
		{"multiple queues with weights and whitespace", "high:3, default:1, low:1 ", map[string]int{"high": 3, "default": 1, "low": 1}},
		{"invalid weight falls back to 1", "custom:invalid", map[string]int{"custom": 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, console.ParseQueueWeights(tc.input))
		})
	}
}

func TestCommand_ValidationAndCancellation(t *testing.T) {
	ctx := context.Background()

	t.Run("serve invalid port returns ValidationError wrapped in UsageError", func(t *testing.T) {
		for _, port := range []string{"-1", "65536"} {
			_, err := execWithTestFactory(ctx, nil, "serve", "--port", port)
			require.Error(t, err)
			var usageErr *console.UsageError
			require.ErrorAs(t, err, &usageErr)
			var valErr *console.ValidationError
			require.ErrorAs(t, err, &valErr)
			assert.Equal(t, "port", valErr.Field)
		}
	})

	t.Run("db:migrate empty env returns ValidationError", func(t *testing.T) {
		_, err := execWithTestFactory(ctx, nil, "db:migrate", "--env", "")
		require.Error(t, err)
		var valErr *console.ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "env", valErr.Field)
	})

	t.Run("db:seed invalid class returns ValidationError", func(t *testing.T) {
		_, err := execWithTestFactory(ctx, nil, "db:seed", "--class", "NonExistentSeeder")
		require.Error(t, err)
		var valErr *console.ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "class", valErr.Field)
	})

	t.Run("db:seed --fresh cancelled via Prompter", func(t *testing.T) {
		mp := newMockPrompter()
		mp.ConfirmResponses = []bool{false}
		_, err := execWithTestFactory(ctx, &testFactory{
			ConfigValue:   config.Default(),
			PrompterValue: mp,
		}, "db:seed", "--fresh")
		require.ErrorIs(t, err, console.ErrCanceled)
	})

	t.Run("db:fresh cancelled via Prompter", func(t *testing.T) {
		mp := newMockPrompter()
		mp.ConfirmResponses = []bool{false}
		_, err := execWithTestFactory(ctx, &testFactory{
			ConfigValue:   config.Default(),
			PrompterValue: mp,
		}, "db:fresh")
		require.ErrorIs(t, err, console.ErrCanceled)
	})

	t.Run("queue:work negative concurrency returns ValidationError", func(t *testing.T) {
		_, err := execWithTestFactory(ctx, nil, "queue:work", "--concurrency", "-1")
		require.Error(t, err)
		var valErr *console.ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "concurrency", valErr.Field)
	})

	t.Run("queue:failed invalid pagination returns ValidationError", func(t *testing.T) {
		_, err := execWithTestFactory(ctx, nil, "queue:failed", "--page", "0")
		require.Error(t, err)
		_, err = execWithTestFactory(ctx, nil, "queue:failed", "--page-size", "-5")
		require.Error(t, err)
	})

	t.Run("queue:retry without arg returns ValidationError", func(t *testing.T) {
		_, err := execWithTestFactory(ctx, nil, "queue:retry")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "please provide a job ID or 'all'")
	})

	t.Run("queue:flush cancelled via Prompter", func(t *testing.T) {
		mp := newMockPrompter()
		mp.ConfirmResponses = []bool{false}
		_, err := execWithTestFactory(ctx, &testFactory{
			ConfigValue:   config.Default(),
			PrompterValue: mp,
		}, "queue:flush")
		require.ErrorIs(t, err, console.ErrCanceled)
	})

	t.Run("scout:import and scout:flush validation", func(t *testing.T) {
		_, err := execWithTestFactory(ctx, nil, "scout:import")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "please specify a model name or use --all")

		_, err = execWithTestFactory(ctx, nil, "scout:import", "--all", "--chunk", "0")
		require.Error(t, err)

		_, err = execWithTestFactory(ctx, nil, "scout:flush")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "please provide a model name to flush")
	})

	t.Run("route:list invalid sort field", func(t *testing.T) {
		for _, s := range []string{"unknown", "status", "uri"} {
			_, err := execWithTestFactory(ctx, nil, "route:list", "--sort", s)
			require.Error(t, err)
			var valErr *console.ValidationError
			require.ErrorAs(t, err, &valErr)
			assert.Equal(t, "sort", valErr.Field)
		}
	})

	t.Run("make:crud cancelled via Prompter on delete", func(t *testing.T) {
		mp := newMockPrompter()
		mp.ConfirmResponses = []bool{false}
		_, err := execWithTestFactory(ctx, &testFactory{PrompterValue: mp}, "make:crud", "article", "-d")
		require.ErrorIs(t, err, console.ErrCanceled)
	})

	t.Run("deprecated space-separated invocation is rejected", func(t *testing.T) {
		_, err := execWithTestFactory(ctx, nil, "queue", "retry")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown command "queue"`)
	})
}

func TestErrCanceled_Export(t *testing.T) {
	assert.Equal(t, "operation canceled by user", console.ErrCanceled.Error())
}
