package core

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"bgin/internal/console/stubs"
)

// GeneratorOptions 封装了代码生成器的入参配置。
type GeneratorOptions struct {
	// Name 是模块名或实体名（支持 CamelCase, snake_case, kebab-case 等格式）
	Name string
	// IsAdmin 标识是否生成管理端模块（默认为 true，路径为 internal/features/admin/<name>/）
	IsAdmin bool
	// TagName 自定义 Swagger 标签名（若留空则自动推导）
	TagName string
	// ChineseName 中文业务名称描述（若留空则默认为 PascalName）
	ChineseName string
	// Force 是否强制覆盖已存在的文件
	Force bool
	// DryRun 是否仅做生成预览而不实际写入文件系统
	DryRun bool
	// RootDir 项目根目录路径（默认为当前工作目录）
	RootDir string
}

// TemplateData 注入到模板渲染上下文的元数据对象。
type TemplateData struct {
	PackageName  string
	PascalName   string
	CamelName    string
	SnakeName    string
	KebabName    string
	PluralSnake  string
	PluralPascal string
	PluralKebab  string
	TableName    string
	TableAlias   string
	TagName      string
	ChineseName  string
	RoutePath    string
	IsAdmin      bool
}

// FileAction 标识单个生成文件的处理结果动作。
type FileAction string

const (
	ActionCreated     FileAction = "CREATED"
	ActionOverwritten FileAction = "OVERWRITTEN"
	ActionSkipped     FileAction = "SKIPPED"
	ActionDryRun      FileAction = "DRY-RUN"
	ActionDeleted     FileAction = "DELETED"
)

// GeneratedResult 记录单个文件的生成详情。
type GeneratedResult struct {
	Path     string
	Template string
	Action   FileAction
	Message  string
}

// BuildTemplateData 根据输入选项计算全部命名衍生变量。
func BuildTemplateData(opts GeneratorOptions) TemplateData {
	raw := strings.TrimSpace(opts.Name)
	snake := ToSnakeCase(raw)
	singularSnake := Singularize(snake)
	pluralSnake := Pluralize(snake)

	pascal := ToPascalCase(singularSnake)
	camel := ToCamelCase(singularSnake)
	kebab := ToKebabCase(singularSnake)
	pluralPascal := ToPascalCase(pluralSnake)
	pluralKebab := ToKebabCase(pluralSnake)

	pkgName := strings.ReplaceAll(singularSnake, "_", "")
	if opts.IsAdmin {
		pkgName = "admin" + pkgName
	}

	chinese := opts.ChineseName
	if chinese == "" {
		chinese = pascal
	}

	tag := opts.TagName
	if tag == "" {
		if opts.IsAdmin {
			tag = chinese + "管理"
		} else {
			tag = chinese
		}
	}

	routePath := "/" + pluralKebab
	if opts.IsAdmin {
		routePath = "/admin/" + pluralKebab
	}

	tableAlias := "t"
	if len(singularSnake) > 0 {
		tableAlias = singularSnake[:1]
	}

	return TemplateData{
		PackageName:  pkgName,
		PascalName:   pascal,
		CamelName:    camel,
		SnakeName:    singularSnake,
		KebabName:    kebab,
		PluralSnake:  pluralSnake,
		PluralPascal: pluralPascal,
		PluralKebab:  pluralKebab,
		TableName:    pluralSnake,
		TableAlias:   tableAlias,
		TagName:      tag,
		ChineseName:  chinese,
		RoutePath:    routePath,
		IsAdmin:      opts.IsAdmin,
	}
}

// PlannedFile 描述待生成文件的路径、模板名与渲染上下文数据。
type PlannedFile struct {
	RelPath  string
	Template string
	Data     any
}

// Plan 描述任意脚手架生成规划契约 (OCP & ISP)。
type Plan interface {
	Name() string
	Files() ([]PlannedFile, error)
	CleanupDirs() []string
}

// CRUDPlan 实现针对 Domain-First CRUD 模块的生成规划契约。
type CRUDPlan struct {
	opts GeneratorOptions
	data TemplateData
}

// NewCRUDPlan 构造针对 CRUD 模块的生成规划。
func NewCRUDPlan(opts GeneratorOptions) *CRUDPlan {
	return &CRUDPlan{
		opts: opts,
		data: BuildTemplateData(opts),
	}
}

// Name 返回当前规划的模块名称。
func (p *CRUDPlan) Name() string {
	return p.opts.Name
}

// Data 返回模板上下文数据。
func (p *CRUDPlan) Data() TemplateData {
	return p.data
}

func (p *CRUDPlan) featureDir() string {
	scope := "client"
	if p.opts.IsAdmin {
		scope = "admin"
	}
	return filepath.Join("internal", "features", scope, p.data.KebabName)
}

// Files 返回 CRUD 规划中全部目标文件及渲染模板。
func (p *CRUDPlan) Files() ([]PlannedFile, error) {
	featureDir := p.featureDir()
	return []PlannedFile{
		{RelPath: filepath.Join("internal", "database", "models", p.data.SnakeName+".go"), Template: "model.tmpl", Data: p.data},
		{RelPath: filepath.Join(featureDir, "dto.go"), Template: "dto.tmpl", Data: p.data},
		{RelPath: filepath.Join(featureDir, "service.go"), Template: "service.tmpl", Data: p.data},
		{RelPath: filepath.Join(featureDir, "handler.go"), Template: "handler.tmpl", Data: p.data},
		{RelPath: filepath.Join(featureDir, "routes.go"), Template: "routes.tmpl", Data: p.data},
	}, nil
}

// CleanupDirs 返回该模块在销毁回退时需要尝试清理的目录列表。
func (p *CRUDPlan) CleanupDirs() []string {
	return []string{p.featureDir()}
}

// Pipeline 提供可插拔的代码生成与回退执行管线引擎 (SRP & OCP)。
type Pipeline struct {
	TemplateFS fs.FS
	RootDir    string
	Force      bool
	DryRun     bool
}

// NewPipeline 构造带有默认配置的生成管线实例。
func NewPipeline(templateFS fs.FS, rootDir string, force, dryRun bool) *Pipeline {
	if templateFS == nil {
		templateFS = stubs.FS
	}
	if rootDir == "" {
		rootDir = "."
	}
	return &Pipeline{
		TemplateFS: templateFS,
		RootDir:    rootDir,
		Force:      force,
		DryRun:     dryRun,
	}
}

// Execute 调度执行指定的生成规划，完成模板读取、渲染、Go格式化、冲突检测与文件写入。
func (p *Pipeline) Execute(plan Plan) ([]GeneratedResult, error) {
	files, err := plan.Files()
	if err != nil {
		return nil, err
	}

	var results []GeneratedResult

	for _, f := range files {
		absPath := filepath.Join(p.RootDir, f.RelPath)
		_, statErr := os.Stat(absPath)
		exists := statErr == nil

		if exists && !p.Force {
			results = append(results, GeneratedResult{
				Path:     f.RelPath,
				Template: f.Template,
				Action:   ActionSkipped,
				Message:  "file already exists, skipped (use --force to overwrite)",
			})
			continue
		}

		tmplContent, err := fs.ReadFile(p.TemplateFS, f.Template)
		if err != nil {
			return results, fmt.Errorf("failed to read template %s: %w", f.Template, err)
		}

		tmpl, err := template.New(f.Template).Parse(string(tmplContent))
		if err != nil {
			return results, fmt.Errorf("failed to parse template %s: %w", f.Template, err)
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, f.Data); err != nil {
			return results, fmt.Errorf("failed to render template %s: %w", f.Template, err)
		}

		formatted := buf.Bytes()
		if strings.HasSuffix(f.RelPath, ".go") {
			if src, err := format.Source(buf.Bytes()); err == nil {
				formatted = src
			}
		}

		if p.DryRun {
			results = append(results, GeneratedResult{
				Path:     f.RelPath,
				Template: f.Template,
				Action:   ActionDryRun,
				Message:  fmt.Sprintf("would write %d bytes", len(formatted)),
			})
			continue
		}

		parentDir := filepath.Dir(absPath)
		if err := os.MkdirAll(parentDir, 0o755); err != nil { //nolint:gosec // 生成物目录需对协作者与 CI 可读
			return results, fmt.Errorf("failed to create directory %s: %w", parentDir, err)
		}

		if err := os.WriteFile(absPath, formatted, 0o644); err != nil { //nolint:gosec // 生成物源码需对协作者与 CI 可读
			return results, fmt.Errorf("failed to write file %s: %w", absPath, err)
		}

		action := ActionCreated
		msg := "successfully generated"
		if exists {
			action = ActionOverwritten
			msg = "overwritten existing file"
		}

		results = append(results, GeneratedResult{
			Path:     f.RelPath,
			Template: f.Template,
			Action:   action,
			Message:  msg,
		})
	}

	return results, nil
}

// Destroy 调度执行指定的规划回退，安全删除生成文件与空目录。
func (p *Pipeline) Destroy(plan Plan) ([]GeneratedResult, error) {
	files, err := plan.Files()
	if err != nil {
		return nil, err
	}

	var results []GeneratedResult

	for _, f := range files {
		absPath := filepath.Join(p.RootDir, f.RelPath)

		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			results = append(results, GeneratedResult{
				Path:     f.RelPath,
				Template: f.Template,
				Action:   ActionSkipped,
				Message:  "file does not exist, skipped",
			})
			continue
		}

		if p.DryRun {
			results = append(results, GeneratedResult{
				Path:     f.RelPath,
				Template: f.Template,
				Action:   ActionDryRun,
				Message:  "would delete file",
			})
			continue
		}

		if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
			return results, fmt.Errorf("failed to delete file %s: %w", absPath, err)
		}

		results = append(results, GeneratedResult{
			Path:     f.RelPath,
			Template: f.Template,
			Action:   ActionDeleted,
			Message:  "successfully deleted",
		})
	}

	if !p.DryRun {
		for _, dir := range plan.CleanupDirs() {
			absDir := filepath.Join(p.RootDir, dir)
			_ = os.Remove(absDir)
		}
	}

	return results, nil
}

// GenerateCRUD 执行完整的 CRUD 模块代码脚手架生成。
func GenerateCRUD(opts GeneratorOptions) ([]GeneratedResult, error) {
	if strings.TrimSpace(opts.Name) == "" {
		return nil, errors.New("module name cannot be empty")
	}

	plan := NewCRUDPlan(opts)
	pipeline := NewPipeline(stubs.FS, opts.RootDir, opts.Force, opts.DryRun)
	return pipeline.Execute(plan)
}

// DestroyCRUD 回退并安全清理指定 CRUD 模块的生成文件与空目录。
func DestroyCRUD(opts GeneratorOptions) ([]GeneratedResult, error) {
	if strings.TrimSpace(opts.Name) == "" {
		return nil, errors.New("module name cannot be empty")
	}

	plan := NewCRUDPlan(opts)
	pipeline := NewPipeline(stubs.FS, opts.RootDir, opts.Force, opts.DryRun)
	return pipeline.Destroy(plan)
}
