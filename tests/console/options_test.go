package console_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/console"
	"bgin/internal/console/core"
)

// TestServeOptions_Validate 测试 HTTP 服务启动参数校验规则。
func TestServeOptions_Validate(t *testing.T) {
	f := &console.TestFactory{}

	tests := []struct {
		name      string
		port      int
		wantError bool
	}{
		{name: "default zero port", port: 0, wantError: false},
		{name: "standard HTTP port", port: 80, wantError: false},
		{name: "custom dev port", port: 8080, wantError: false},
		{name: "maximum port boundary", port: 65535, wantError: false},
		{name: "negative port invalid", port: -1, wantError: true},
		{name: "out of range port invalid", port: 65536, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := console.NewServeOptions(f)
			opts.Port = tt.port
			err := opts.Validate()
			if tt.wantError {
				require.Error(t, err)
				var valErr *console.ValidationError
				require.ErrorAs(t, err, &valErr)
				assert.Equal(t, "port", valErr.Field)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestJWTSecretOptions 测试 JWT 密钥生成选项与纯内存执行。
func TestJWTSecretOptions(t *testing.T) {
	ctx := context.Background()

	t.Run("validate empty envPath returns error", func(t *testing.T) {
		opts := console.NewJWTSecretOptions(&console.TestFactory{})
		opts.EnvPath = ""
		err := opts.Validate()
		require.Error(t, err)
		var valErr *console.ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "envPath", valErr.Field)
	})

	t.Run("show secret writes to IOStreams without touching filesystem", func(t *testing.T) {
		streams, _, stdoutBuf, _ := core.TestIOStreams()
		f := &console.TestFactory{StreamsValue: streams}

		opts := console.NewJWTSecretOptions(f)
		opts.Show = true
		require.NoError(t, opts.Validate())
		require.NoError(t, opts.Run(ctx))

		secret := strings.TrimSpace(stdoutBuf.String())
		assert.NotEmpty(t, secret)
		decoded, err := base64.StdEncoding.DecodeString(secret)
		require.NoError(t, err)
		assert.Len(t, decoded, 32)
	})
}

// TestDBOptions_Validate 测试数据库管理命令选项校验。
func TestDBOptions_Validate(t *testing.T) {
	f := &console.TestFactory{}

	t.Run("db:migrate options validation", func(t *testing.T) {
		opts := console.NewDBMigrateOptions(f)
		require.NoError(t, opts.Validate())

		opts.Env = ""
		err := opts.Validate()
		require.Error(t, err)
		var valErr *console.ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "env", valErr.Field)
	})

	t.Run("db:seed options validation", func(t *testing.T) {
		opts := console.NewDBSeedOptions(f)
		require.NoError(t, opts.Complete(nil, nil))
		assert.Equal(t, "DatabaseSeeder", opts.ClassName)
		require.NoError(t, opts.Validate())

		opts.ClassName = "NonExistentSeeder"
		err := opts.Validate()
		require.Error(t, err)
		var valErr *console.ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "class", valErr.Field)
	})

	t.Run("db:fresh cancelled via Prompter", func(t *testing.T) {
		mockPrompter := core.NewMockPrompter()
		mockPrompter.ConfirmResponses = []bool{false}

		cfg := config.Default()
		f := &console.TestFactory{
			ConfigValue:   cfg,
			PrompterValue: mockPrompter,
		}

		opts := console.NewDBFreshOptions(f)
		opts.Force = false
		err := opts.Run(context.Background())
		require.ErrorIs(t, err, core.ErrCanceled)
	})
}

// TestQueueOptions_Validate 测试异步队列命令入参约束。
func TestQueueOptions_Validate(t *testing.T) {
	f := &console.TestFactory{}

	t.Run("queue:work validate concurrency", func(t *testing.T) {
		opts := console.NewQueueWorkOptions(f)
		require.NoError(t, opts.Validate())

		opts.Concurrency = -1
		err := opts.Validate()
		require.Error(t, err)
		var valErr *console.ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "concurrency", valErr.Field)
	})

	t.Run("queue:failed validate pagination", func(t *testing.T) {
		opts := console.NewQueueFailedOptions(f)
		require.NoError(t, opts.Validate())

		opts.Page = 0
		err := opts.Validate()
		require.Error(t, err)

		opts.Page = 1
		opts.PageSize = -5
		err = opts.Validate()
		require.Error(t, err)
	})

	t.Run("queue:retry validate target", func(t *testing.T) {
		opts := console.NewQueueRetryOptions(f)
		err := opts.Validate()
		require.Error(t, err)

		require.NoError(t, opts.Complete(nil, []string{"all"}))
		assert.Equal(t, "all", opts.Target)
		require.NoError(t, opts.Validate())
	})

	t.Run("queue:flush cancelled via Prompter", func(t *testing.T) {
		mockPrompter := core.NewMockPrompter()
		mockPrompter.ConfirmResponses = []bool{false}

		f := &console.TestFactory{
			ConfigValue:   config.Default(),
			PrompterValue: mockPrompter,
		}

		opts := console.NewQueueFlushOptions(f)
		opts.Force = false
		err := opts.Run(context.Background())
		require.ErrorIs(t, err, core.ErrCanceled)
	})
}

// TestRouteListOptions_Validate 测试路由检查命令排序参数。
func TestRouteListOptions_Validate(t *testing.T) {
	f := &console.TestFactory{}

	validSorts := []string{"path", "method", "handler", "PATH", "Method", ""}
	for _, s := range validSorts {
		opts := console.NewRouteListOptions(f)
		opts.SortField = s
		require.NoError(t, opts.Validate(), "SortField: "+s)
	}

	invalidSorts := []string{"unknown", "status", "uri"}
	for _, s := range invalidSorts {
		opts := console.NewRouteListOptions(f)
		opts.SortField = s
		err := opts.Validate()
		require.Error(t, err, "SortField should be invalid: "+s)
		var valErr *console.ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "sort", valErr.Field)
	}
}

// TestScoutOptions_Validate 测试搜索引擎相关命令选项。
func TestScoutOptions_Validate(t *testing.T) {
	f := &console.TestFactory{}

	t.Run("scout:import validation", func(t *testing.T) {
		opts := console.NewScoutImportOptions(f)
		err := opts.Validate()
		require.Error(t, err)

		opts.All = true
		require.NoError(t, opts.Validate())

		opts.All = false
		opts.Model = "non_existent_search_model"
		err = opts.Validate()
		require.Error(t, err)

		opts.ChunkSize = 0
		opts.All = true
		err = opts.Validate()
		require.Error(t, err)
	})

	t.Run("scout:flush cancelled via Prompter", func(t *testing.T) {
		mockPrompter := core.NewMockPrompter()
		mockPrompter.ConfirmResponses = []bool{false}

		cfg := config.Default()
		cfg.Search.Driver = "meilisearch"
		cfg.Search.Meilisearch.Host = "http://localhost:7700"

		f := &console.TestFactory{
			ConfigValue:   cfg,
			PrompterValue: mockPrompter,
		}

		opts := console.NewScoutFlushOptions(f)
		require.Error(t, opts.Validate())
	})
}

// TestMakeCRUDOptions_ValidateAndPrompter 测试 CRUD 代码生成选项与提示集成。
func TestMakeCRUDOptions_ValidateAndPrompter(t *testing.T) {
	f := &console.TestFactory{}

	t.Run("validate requires name", func(t *testing.T) {
		opts := console.NewMakeCRUDOptions(f)
		err := opts.Validate()
		require.Error(t, err)
		var valErr *console.ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "name", valErr.Field)

		opts.Name = "article"
		require.NoError(t, opts.Validate())
	})

	t.Run("destroy cancelled via Prompter", func(t *testing.T) {
		mockPrompter := core.NewMockPrompter()
		mockPrompter.ConfirmResponses = []bool{false}

		fWithPrompter := &console.TestFactory{PrompterValue: mockPrompter}
		opts := console.NewMakeCRUDOptions(fWithPrompter)
		opts.Name = "article"
		opts.Delete = true
		opts.Force = false
		opts.DryRun = false

		err := opts.Run(context.Background())
		require.ErrorIs(t, err, core.ErrCanceled)
	})
}

// TestGeneratorPipeline_CustomPlan 验证生成管线引擎可接入自定义规划 (OCP 扩展性测试)。
func TestGeneratorPipeline_CustomPlan(t *testing.T) {
	mockFS := fstest.MapFS{
		"sample.tmpl": &fstest.MapFile{
			Data: []byte("Hello {{.Name}}!"),
		},
	}

	tempDir := t.TempDir()
	pipeline := core.NewPipeline(mockFS, tempDir, false, false)

	// 定义符合 Plan 契约的自定义业务规划
	customPlan := &testCustomPlan{
		name: "greeter",
	}

	results, err := pipeline.Execute(customPlan)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, core.ActionCreated, results[0].Action)

	// 销毁回退测试
	destroyResults, err := pipeline.Destroy(customPlan)
	require.NoError(t, err)
	require.Len(t, destroyResults, 1)
	assert.Equal(t, core.ActionDeleted, destroyResults[0].Action)
}

type testCustomPlan struct {
	name string
}

func (p *testCustomPlan) Name() string {
	return p.name
}

func (p *testCustomPlan) Files() ([]core.PlannedFile, error) {
	return []core.PlannedFile{
		{
			RelPath:  "greeter.txt",
			Template: "sample.tmpl",
			Data:     map[string]string{"Name": "World"},
		},
	}, nil
}

func (p *testCustomPlan) CleanupDirs() []string {
	return nil
}
