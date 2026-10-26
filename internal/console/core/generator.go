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

	"cgin/internal/console/stubs"
)

// GeneratorOptions 封装了代码生成器的入参配置。
type GeneratorOptions struct {
	// Name 是模块名或实体名（支持 CamelCase, snake_case, kebab-case 等格式）
	Name string
	// IsAdmin 标识是否生成管理端模块（默认为 true，落盘到各层的 admin/ 子目录）
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
	// ModuleFile 是该模块在各层落盘的统一文件名。
	ModuleFile string
	// LayerDir 是管理端专属子目录名（admin）；客户端为空字符串。
	LayerDir string
	// DTOImportPath、HandlerImportPath、ServiceImportPath 是三层对应的导入路径。
	DTOImportPath     string
	HandlerImportPath string
	ServiceImportPath string
	// RoutesPath 是路由注册文件的相对路径。
	RoutesPath string
	// DTOPackage、HandlerPackage、ServicePackage、RoutesPackage 是四层各自的包名。
	DTOPackage     string
	HandlerPackage string
	ServicePackage string
	RoutesPackage  string
	// ScopePascal 是作用域首字母大写形式（Admin/Client），用于拼接全局唯一的路由注册函数名。
	ScopePascal string
	// RoutesFunc 是注册到 internal/routes 单包后的唯一函数名。
	RoutesFunc   string
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

	basePkg := strings.ReplaceAll(singularSnake, "_", "")
	moduleFile := basePkg + ".go"
	scopePrefix := ""
	dtoPackage := "dto"
	handlerPackage := "handler"
	servicePackage := "service"
	routesPackage := "routes"
	routesPath := filepath.Join("internal", "routes", moduleFile)
	if opts.IsAdmin {
		scopePrefix = "admin"
		dtoPackage = "admindto"
		handlerPackage = "adminhandler"
		servicePackage = "adminservice"
		routesPackage = "adminroutes"
		routesPath = filepath.Join("internal", "routes", "admin", moduleFile)
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

	scopePascal := "Client"
	if opts.IsAdmin {
		scopePascal = "Admin"
	}

	return TemplateData{
		ModuleFile:        moduleFile,
		LayerDir:          scopePrefix,
		DTOImportPath:     filepath.Join("cgin", "internal", "dto", scopePrefix),
		HandlerImportPath: filepath.Join("cgin", "internal", "handler", scopePrefix),
		ServiceImportPath: filepath.Join("cgin", "internal", "service", scopePrefix),
		RoutesPath:        routesPath,
		DTOPackage:        dtoPackage,
		HandlerPackage:    handlerPackage,
		ServicePackage:    servicePackage,
		RoutesPackage:     routesPackage,
		ScopePascal:       scopePascal,
		RoutesFunc:        "Register" + scopePascal + pluralPascal + "Routes",
		PascalName:        pascal,
		CamelName:         camel,
		SnakeName:         singularSnake,
		KebabName:         kebab,
		PluralSnake:       pluralSnake,
		PluralPascal:      pluralPascal,
		PluralKebab:       pluralKebab,
		TableName:         pluralSnake,
		TableAlias:        tableAlias,
		TagName:           tag,
		ChineseName:       chinese,
		RoutePath:         routePath,
		IsAdmin:           opts.IsAdmin,
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

// CRUDPlan 实现针对分层优先（routes → handler → service → dto）CRUD 模块的生成规划契约。
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

// moduleDirs 返回该模块在 dto / handler / service 三层的目录路径。
func (p *CRUDPlan) moduleDirs() (dtoDir, handlerDir, serviceDir string) {
	return filepath.Join("internal", "dto", p.data.LayerDir),
		filepath.Join("internal", "handler", p.data.LayerDir),
		filepath.Join("internal", "service", p.data.LayerDir)
}

// Files 返回 CRUD 规划中全部目标文件及渲染模板。
func (p *CRUDPlan) Files() ([]PlannedFile, error) {
	dtoDir, handlerDir, serviceDir := p.moduleDirs()
	return []PlannedFile{
		{RelPath: filepath.Join("database", "queries", p.data.PluralSnake+".sql"), Template: "queries.tmpl", Data: p.data},
		{RelPath: filepath.Join("database", "schemas", p.data.PluralSnake+".sql"), Template: "schema.tmpl", Data: p.data},
		{RelPath: filepath.Join(dtoDir, p.data.ModuleFile), Template: "dto.tmpl", Data: p.data},
		{RelPath: filepath.Join(serviceDir, p.data.ModuleFile), Template: "service.tmpl", Data: p.data},
		{RelPath: filepath.Join(handlerDir, p.data.ModuleFile), Template: "handler.tmpl", Data: p.data},
		{RelPath: p.data.RoutesPath, Template: "routes.tmpl", Data: p.data},
	}, nil
}

// CleanupDirs 返回该模块在销毁回退时需要尝试清理的目录列表。
func (p *CRUDPlan) CleanupDirs() []string {
	dtoDir, handlerDir, serviceDir := p.moduleDirs()
	return []string{dtoDir, handlerDir, serviceDir}
}

// Pipeline 提供可插拔的代码生成与回退执行管线引擎 (SRP & OCP)。
type Pipeline struct {
	TemplateFS fs.FS
	RootDir    string
	Force      bool
	DryRun     bool
}

// NewPipeline 构造带有默认配置的生成管线实例。
//
// force 表示覆盖已存在文件；dryRun 表示只输出计划、不落盘。
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

	results := make([]GeneratedResult, 0, len(files))
	for _, file := range files {
		result, err := p.executeFile(file)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

// executeFile 生成单个文件并返回处理结果。
func (p *Pipeline) executeFile(file PlannedFile) (GeneratedResult, error) {
	absPath := filepath.Join(p.RootDir, file.RelPath)
	_, statErr := os.Stat(absPath)
	exists := statErr == nil

	if exists && !p.Force {
		return generatedResult(file, ActionSkipped, "file already exists, skipped (use --force to overwrite)"), nil
	}

	formatted, err := p.render(file)
	if err != nil {
		return GeneratedResult{}, err
	}

	if p.DryRun {
		return generatedResult(file, ActionDryRun, fmt.Sprintf("would write %d bytes", len(formatted))), nil
	}

	parentDir := filepath.Dir(absPath)
	if err := os.MkdirAll(parentDir, 0o755); err != nil { //nolint:gosec // 生成物目录需对协作者与 CI 可读
		return GeneratedResult{}, fmt.Errorf("failed to create directory %s: %w", parentDir, err)
	}

	if err := os.WriteFile(absPath, formatted, 0o644); err != nil { //nolint:gosec // 生成物源码需对协作者与 CI 可读
		return GeneratedResult{}, fmt.Errorf("failed to write file %s: %w", absPath, err)
	}

	if exists {
		return generatedResult(file, ActionOverwritten, "overwritten existing file"), nil
	}
	return generatedResult(file, ActionCreated, "successfully generated"), nil
}

// render 读取模板、渲染内容，并对 Go 文件执行源码格式化。
func (p *Pipeline) render(file PlannedFile) ([]byte, error) {
	content, err := fs.ReadFile(p.TemplateFS, file.Template)
	if err != nil {
		return nil, fmt.Errorf("failed to read template %s: %w", file.Template, err)
	}

	tmpl, err := template.New(file.Template).Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse template %s: %w", file.Template, err)
	}

	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, file.Data); err != nil {
		return nil, fmt.Errorf("failed to render template %s: %w", file.Template, err)
	}
	if !strings.HasSuffix(file.RelPath, ".go") {
		return buffer.Bytes(), nil
	}

	// 格式化失败时保留原始渲染结果，与既有行为一致。
	if formatted, err := format.Source(buffer.Bytes()); err == nil {
		return formatted, nil
	}
	return buffer.Bytes(), nil
}

// Destroy 调度执行指定的规划回退，安全删除生成文件与空目录。
func (p *Pipeline) Destroy(plan Plan) ([]GeneratedResult, error) {
	files, err := plan.Files()
	if err != nil {
		return nil, err
	}

	results := make([]GeneratedResult, 0, len(files))
	for _, file := range files {
		result, err := p.destroyFile(file)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}

	if !p.DryRun {
		for _, dir := range plan.CleanupDirs() {
			_ = os.Remove(filepath.Join(p.RootDir, dir))
		}
	}
	return results, nil
}

// destroyFile 删除单个生成文件并返回处理结果。
func (p *Pipeline) destroyFile(file PlannedFile) (GeneratedResult, error) {
	absPath := filepath.Join(p.RootDir, file.RelPath)

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return generatedResult(file, ActionSkipped, "file does not exist, skipped"), nil
	}
	if p.DryRun {
		return generatedResult(file, ActionDryRun, "would delete file"), nil
	}

	if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
		return GeneratedResult{}, fmt.Errorf("failed to delete file %s: %w", absPath, err)
	}
	return generatedResult(file, ActionDeleted, "successfully deleted"), nil
}

// generatedResult 构造单个文件的生成结果。
func generatedResult(file PlannedFile, action FileAction, message string) GeneratedResult {
	return GeneratedResult{
		Path:     file.RelPath,
		Template: file.Template,
		Action:   action,
		Message:  message,
	}
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
