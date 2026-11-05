package console_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/console"
	"cgin/internal/console/scaffold"
)

func TestNamingUtilities(t *testing.T) {
	tests := []struct {
		input        string
		wantSnake    string
		wantPascal   string
		wantCamel    string
		wantKebab    string
		wantPlural   string
		wantSingular string
	}{
		{"article", "article", "Article", "article", "article", "articles", "article"},
		{"category", "category", "Category", "category", "category", "categories", "category"},
		{"article_category", "article_category", "ArticleCategory", "articleCategory", "article-category", "article_categories", "article_category"},
		{"ArticleCategory", "article_category", "ArticleCategory", "articleCategory", "article-category", "article_categories", "article_category"},
		{"status", "status", "Status", "status", "status", "statuses", "status"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.wantSnake, scaffold.ToSnakeCase(tt.input))
			assert.Equal(t, tt.wantPascal, scaffold.ToPascalCase(tt.input))
			assert.Equal(t, tt.wantCamel, scaffold.ToCamelCase(tt.input))
			assert.Equal(t, tt.wantKebab, scaffold.ToKebabCase(tt.input))
			assert.Equal(t, tt.wantPlural, scaffold.Pluralize(scaffold.ToSnakeCase(tt.input)))
			assert.Equal(t, tt.wantSingular, scaffold.Singularize(tt.wantPlural))
		})
	}
}

func TestGenerateCRUD_Execution(t *testing.T) {
	tempDir := t.TempDir()

	opts := scaffold.GeneratorOptions{
		Name:        "article",
		IsAdmin:     true,
		TagName:     "文章管理",
		ChineseName: "文章",
		RootDir:     tempDir,
	}

	results, err := scaffold.GenerateCRUD(opts)
	require.NoError(t, err)
	require.Len(t, results, 6)

	for _, r := range results {
		assert.Equal(t, scaffold.ActionCreated, r.Action)
		fullPath := filepath.Join(tempDir, r.Path)
		info, statErr := os.Stat(fullPath)
		require.NoError(t, statErr)
		assert.False(t, info.IsDir())

		content, readErr := os.ReadFile(fullPath)
		require.NoError(t, readErr)
		assert.NotEmpty(t, content)
	}

	dtoPath := filepath.Join(tempDir, "internal", "feature", "admin", "article", "v1", "dto.go")
	dtoContent, err := os.ReadFile(dtoPath)
	require.NoError(t, err)
	assert.Contains(t, string(dtoContent), "ArticlePathParams")
	assert.Contains(t, string(dtoContent), "ListArticleQueryParams")
	assert.Contains(t, string(dtoContent), "ArticleResource")

	servicePath := filepath.Join(tempDir, "internal", "feature", "admin", "article", "service.go")
	serviceContent, err := os.ReadFile(servicePath)
	require.NoError(t, err)
	assert.Contains(t, string(serviceContent), "package adminarticle")
	assert.Contains(t, string(serviceContent), "type ArticleService interface")

	handlerPath := filepath.Join(tempDir, "internal", "feature", "admin", "article", "v1", "handler.go")
	handlerContent, err := os.ReadFile(handlerPath)
	require.NoError(t, err)
	assert.Contains(t, string(handlerContent), "package v1")
	assert.Contains(t, string(handlerContent), "response.Paginated(c, resources, meta)")
	assert.Contains(t, string(handlerContent), "@Tags         文章管理")

	routesPath := filepath.Join(tempDir, "internal", "feature", "admin", "article", "v1", "router.go")
	routesContent, err := os.ReadFile(routesPath)
	require.NoError(t, err)
	assert.Contains(t, string(routesContent), "RegisterRoutes")
	assert.Contains(t, string(routesContent), "package v1")
	assert.Contains(t, string(routesContent), `mw.Can(authorization.Resource("articles")`)

	queriesPath := filepath.Join(tempDir, "database", "queries", "articles.sql")
	queriesContent, err := os.ReadFile(queriesPath)
	require.NoError(t, err)
	assert.Contains(t, string(queriesContent), "name: ListArticles :many")
	assert.Contains(t, string(queriesContent), "sqlc.embed(a)")

	schemaPath := filepath.Join(tempDir, "database", "schemas", "articles.sql")
	schemaContent, err := os.ReadFile(schemaPath)
	require.NoError(t, err)
	assert.Contains(t, string(schemaContent), "CREATE TABLE IF NOT EXISTS articles")

	results2, err := scaffold.GenerateCRUD(opts)
	require.NoError(t, err)
	require.Len(t, results2, 6)
	for _, r := range results2 {
		assert.Equal(t, scaffold.ActionSkipped, r.Action)
	}

	opts.Force = true
	results3, err := scaffold.GenerateCRUD(opts)
	require.NoError(t, err)
	require.Len(t, results3, 6)
	for _, r := range results3 {
		assert.Equal(t, scaffold.ActionOverwritten, r.Action)
	}

	opts.DryRun = true
	results4, err := scaffold.GenerateCRUD(opts)
	require.NoError(t, err)
	require.Len(t, results4, 6)
	for _, r := range results4 {
		assert.Equal(t, scaffold.ActionDryRun, r.Action)
	}
}

func TestMakeCommand_CLIExecution(t *testing.T) {
	ctx := context.Background()

	err := console.Execute(ctx, []string{"make", "crud", "article", "--dry-run"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown command "make"`)

	err = console.Execute(ctx, []string{"make:crud", "article", "--dry-run"})
	require.NoError(t, err)

	err = console.Execute(ctx, []string{"make:crud"})
	require.Error(t, err)
}

func TestDestroyCRUD_Execution(t *testing.T) {
	tempDir := t.TempDir()

	opts := scaffold.GeneratorOptions{
		Name:        "article",
		IsAdmin:     true,
		TagName:     "文章管理",
		ChineseName: "文章",
		RootDir:     tempDir,
	}

	results, err := scaffold.GenerateCRUD(opts)
	require.NoError(t, err)
	require.Len(t, results, 6)

	opts.DryRun = true
	destroyDryRun, err := scaffold.DestroyCRUD(opts)
	require.NoError(t, err)
	require.Len(t, destroyDryRun, 6)
	for _, r := range destroyDryRun {
		assert.Equal(t, scaffold.ActionDryRun, r.Action)
	}

	handlerPath := filepath.Join(tempDir, "internal", "feature", "admin", "article", "v1", "handler.go")
	_, err = os.Stat(handlerPath)
	require.NoError(t, err)

	opts.DryRun = false
	destroyResults, err := scaffold.DestroyCRUD(opts)
	require.NoError(t, err)
	require.Len(t, destroyResults, 6)
	for _, r := range destroyResults {
		assert.Equal(t, scaffold.ActionDeleted, r.Action)
		fullPath := filepath.Join(tempDir, r.Path)
		_, statErr := os.Stat(fullPath)
		assert.True(t, os.IsNotExist(statErr))
	}

	featureDir := filepath.Join(tempDir, "internal", "feature", "admin", "article")
	_, statErr := os.Stat(featureDir)
	assert.True(t, os.IsNotExist(statErr))

	destroyAgain, err := scaffold.DestroyCRUD(opts)
	require.NoError(t, err)
	require.Len(t, destroyAgain, 6)
	for _, r := range destroyAgain {
		assert.Equal(t, scaffold.ActionSkipped, r.Action)
	}
}

func TestMakeDestroyCommand_CLIExecution(t *testing.T) {
	ctx := context.Background()

	err := console.Execute(ctx, []string{"make", "destroy", "article", "--dry-run"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown command "make"`)

	err = console.Execute(ctx, []string{"make:destroy", "article", "--dry-run"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown command "make:destroy"`)

	err = console.Execute(ctx, []string{"make:crud", "article", "-d", "--dry-run"})
	require.NoError(t, err)

	err = console.Execute(ctx, []string{"make:crud", "article", "--delete", "--dry-run"})
	require.NoError(t, err)

	err = console.Execute(ctx, []string{"make:crud", "-d"})
	require.Error(t, err)
}

func TestGeneratorPipeline_CustomPlan(t *testing.T) {
	mockFS := fstest.MapFS{
		"sample.tmpl": &fstest.MapFile{
			Data: []byte("Hello {{.Name}}!"),
		},
	}

	tempDir := t.TempDir()
	pipeline := scaffold.NewPipeline(mockFS, tempDir, false, false)
	customPlan := &testCustomPlan{name: "greeter"}

	results, err := pipeline.Execute(customPlan)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, scaffold.ActionCreated, results[0].Action)

	destroyResults, err := pipeline.Destroy(customPlan)
	require.NoError(t, err)
	require.Len(t, destroyResults, 1)
	assert.Equal(t, scaffold.ActionDeleted, destroyResults[0].Action)
}

type testCustomPlan struct {
	name string
}

func (p *testCustomPlan) Name() string { return p.name }
func (p *testCustomPlan) Files() ([]scaffold.PlannedFile, error) {
	return []scaffold.PlannedFile{
		{
			RelPath:  "greeter.txt",
			Template: "sample.tmpl",
			Data:     map[string]string{"Name": "World"},
		},
	}, nil
}
func (p *testCustomPlan) CleanupDirs() []string { return nil }
