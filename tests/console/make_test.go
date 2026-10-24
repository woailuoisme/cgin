package console_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/console"
	"bgin/internal/console/core"
)

// TestNamingUtilities 测试命名转换与单复数处理。
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
		{
			input:        "article",
			wantSnake:    "article",
			wantPascal:   "Article",
			wantCamel:    "article",
			wantKebab:    "article",
			wantPlural:   "articles",
			wantSingular: "article",
		},
		{
			input:        "category",
			wantSnake:    "category",
			wantPascal:   "Category",
			wantCamel:    "category",
			wantKebab:    "category",
			wantPlural:   "categories",
			wantSingular: "category",
		},
		{
			input:        "article_category",
			wantSnake:    "article_category",
			wantPascal:   "ArticleCategory",
			wantCamel:    "articleCategory",
			wantKebab:    "article-category",
			wantPlural:   "article_categories",
			wantSingular: "article_category",
		},
		{
			input:        "ArticleCategory",
			wantSnake:    "article_category",
			wantPascal:   "ArticleCategory",
			wantCamel:    "articleCategory",
			wantKebab:    "article-category",
			wantPlural:   "article_categories",
			wantSingular: "article_category",
		},
		{
			input:        "status",
			wantSnake:    "status",
			wantPascal:   "Status",
			wantCamel:    "status",
			wantKebab:    "status",
			wantPlural:   "statuses",
			wantSingular: "status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.wantSnake, core.ToSnakeCase(tt.input))
			assert.Equal(t, tt.wantPascal, core.ToPascalCase(tt.input))
			assert.Equal(t, tt.wantCamel, core.ToCamelCase(tt.input))
			assert.Equal(t, tt.wantKebab, core.ToKebabCase(tt.input))
			assert.Equal(t, tt.wantPlural, core.Pluralize(core.ToSnakeCase(tt.input)))
			assert.Equal(t, tt.wantSingular, core.Singularize(tt.wantPlural))
		})
	}
}

// TestGenerateCRUD_Execution 验证代码脚手架在临时目录中的全量生成闭环。
func TestGenerateCRUD_Execution(t *testing.T) {
	tempDir := t.TempDir()

	opts := core.GeneratorOptions{
		Name:        "article",
		IsAdmin:     true,
		TagName:     "文章管理",
		ChineseName: "文章",
		RootDir:     tempDir,
	}

	// 1. 初次生成
	results, err := core.GenerateCRUD(opts)
	require.NoError(t, err)
	require.Len(t, results, 5)

	for _, r := range results {
		assert.Equal(t, core.ActionCreated, r.Action)
		fullPath := filepath.Join(tempDir, r.Path)
		info, statErr := os.Stat(fullPath)
		require.NoError(t, statErr)
		assert.False(t, info.IsDir())

		content, readErr := os.ReadFile(fullPath)
		require.NoError(t, readErr)
		assert.NotEmpty(t, content)
	}

	// 2. 校验特定关键代码模式
	dtoPath := filepath.Join(tempDir, "internal", "features", "admin", "article", "dto.go")
	dtoContent, err := os.ReadFile(dtoPath)
	require.NoError(t, err)
	assert.Contains(t, string(dtoContent), "ArticlePathParams")
	assert.Contains(t, string(dtoContent), "ListArticleQueryParams")
	assert.Contains(t, string(dtoContent), "ArticleResource")

	handlerPath := filepath.Join(tempDir, "internal", "features", "admin", "article", "handler.go")
	handlerContent, err := os.ReadFile(handlerPath)
	require.NoError(t, err)
	assert.Contains(t, string(handlerContent), "package adminarticle")
	assert.Contains(t, string(handlerContent), "response.Paginated(c, items, meta)")
	assert.Contains(t, string(handlerContent), "@Tags         文章管理")

	routesPath := filepath.Join(tempDir, "internal", "features", "admin", "article", "routes.go")
	routesContent, err := os.ReadFile(routesPath)
	require.NoError(t, err)
	assert.Contains(t, string(routesContent), "RegisterAdminRoutes")
	assert.Contains(t, string(routesContent), `mw.Can(authorization.Resource("articles")`)

	modelPath := filepath.Join(tempDir, "internal", "database", "models", "article.go")
	modelContent, err := os.ReadFile(modelPath)
	require.NoError(t, err)
	assert.Contains(t, string(modelContent), `bun:"table:articles,alias:a"`)
	assert.Contains(t, string(modelContent), "type Article struct")

	// 3. 不带 force 再次生成，应当被全部跳过
	results2, err := core.GenerateCRUD(opts)
	require.NoError(t, err)
	require.Len(t, results2, 5)
	for _, r := range results2 {
		assert.Equal(t, core.ActionSkipped, r.Action)
	}

	// 4. 携带 force 标志再次生成，应当覆盖
	opts.Force = true
	results3, err := core.GenerateCRUD(opts)
	require.NoError(t, err)
	require.Len(t, results3, 5)
	for _, r := range results3 {
		assert.Equal(t, core.ActionOverwritten, r.Action)
	}

	// 5. Dry-run 模拟运行不影响文件
	opts.DryRun = true
	results4, err := core.GenerateCRUD(opts)
	require.NoError(t, err)
	require.Len(t, results4, 5)
	for _, r := range results4 {
		assert.Equal(t, core.ActionDryRun, r.Action)
	}
}

// TestMakeCommand_CLIExecution 验证通过 Cobra 命令入口执行 dry-run。
func TestMakeCommand_CLIExecution(t *testing.T) {
	ctx := context.Background()

	// 旧空格子命令格式已被废除，必须报错：bgin make crud article --dry-run
	err := console.Execute(ctx, []string{"make", "crud", "article", "--dry-run"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown command "make"`)

	// 测试 Artisan 冒号命令：bgin make:crud article --dry-run
	err = console.Execute(ctx, []string{"make:crud", "article", "--dry-run"})
	require.NoError(t, err)

	// 缺少必填参数时应报错
	err = console.Execute(ctx, []string{"make:crud"})
	require.Error(t, err)
}

// TestDestroyCRUD_Execution 验证代码脚手架在临时目录中的回退清理闭环。
func TestDestroyCRUD_Execution(t *testing.T) {
	tempDir := t.TempDir()

	opts := core.GeneratorOptions{
		Name:        "article",
		IsAdmin:     true,
		TagName:     "文章管理",
		ChineseName: "文章",
		RootDir:     tempDir,
	}

	// 1. 先生成文件
	results, err := core.GenerateCRUD(opts)
	require.NoError(t, err)
	require.Len(t, results, 5)

	// 2. Dry-run 回退删除
	opts.DryRun = true
	destroyDryRun, err := core.DestroyCRUD(opts)
	require.NoError(t, err)
	require.Len(t, destroyDryRun, 5)
	for _, r := range destroyDryRun {
		assert.Equal(t, core.ActionDryRun, r.Action)
	}

	// 验证文件依然存在
	handlerPath := filepath.Join(tempDir, "internal", "features", "admin", "article", "handler.go")
	_, err = os.Stat(handlerPath)
	require.NoError(t, err)

	// 3. 真实回退删除
	opts.DryRun = false
	destroyResults, err := core.DestroyCRUD(opts)
	require.NoError(t, err)
	require.Len(t, destroyResults, 5)
	for _, r := range destroyResults {
		assert.Equal(t, core.ActionDeleted, r.Action)
	}

	// 验证生成的文件已被物理删除
	for _, r := range destroyResults {
		fullPath := filepath.Join(tempDir, r.Path)
		_, statErr := os.Stat(fullPath)
		assert.True(t, os.IsNotExist(statErr), "File should be deleted: "+fullPath)
	}

	// 验证空的 feature 目录也被清理
	featureDir := filepath.Join(tempDir, "internal", "features", "admin", "article")
	_, statErr := os.Stat(featureDir)
	assert.True(t, os.IsNotExist(statErr), "Feature directory should be removed: "+featureDir)

	// 4. 重复执行回退删除，应安全跳过（ActionSkipped）
	destroyAgain, err := core.DestroyCRUD(opts)
	require.NoError(t, err)
	require.Len(t, destroyAgain, 5)
	for _, r := range destroyAgain {
		assert.Equal(t, core.ActionSkipped, r.Action)
	}
}

// TestMakeDestroyCommand_CLIExecution 验证通过 make:crud -d / --delete 执行回退删除 dry-run 与废弃命令拦截。
func TestMakeDestroyCommand_CLIExecution(t *testing.T) {
	ctx := context.Background()

	// 1. 旧空格子命令格式已被废除，必须报错：bgin make destroy article --dry-run
	err := console.Execute(ctx, []string{"make", "destroy", "article", "--dry-run"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown command "make"`)

	// 2. 原 make:destroy 命令已被彻底移除，必须报错：bgin make:destroy
	err = console.Execute(ctx, []string{"make:destroy", "article", "--dry-run"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown command "make:destroy"`)

	// 3. 测试通过 make:crud -d 进行回退删除 dry-run
	err = console.Execute(ctx, []string{"make:crud", "article", "-d", "--dry-run"})
	require.NoError(t, err)

	// 4. 测试通过 make:crud --delete 进行回退删除 dry-run
	err = console.Execute(ctx, []string{"make:crud", "article", "--delete", "--dry-run"})
	require.NoError(t, err)

	// 5. 缺少必填参数时在非 TTY 下应报错
	err = console.Execute(ctx, []string{"make:crud", "-d"})
	require.Error(t, err)
}
