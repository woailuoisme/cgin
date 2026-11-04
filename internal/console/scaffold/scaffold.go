package scaffold

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"unicode"
)

//go:embed stubs/*.tmpl
var embeddedStubs embed.FS

func defaultTemplateFS() fs.FS {
	sub, err := fs.Sub(embeddedStubs, "stubs")
	if err != nil {
		return embeddedStubs
	}
	return sub
}

// GeneratorOptions 封装代码生成器的入参配置。
type GeneratorOptions struct {
	Name        string
	IsAdmin     bool
	TagName     string
	ChineseName string
	Force       bool
	DryRun      bool
	RootDir     string
}

// TemplateData 注入到模板渲染上下文的元数据对象。
type TemplateData struct {
	ModuleFile        string
	LayerDir          string
	DTOImportPath     string
	HandlerImportPath string
	ServiceImportPath string
	RoutesPath        string
	DTOPackage        string
	HandlerPackage    string
	ServicePackage    string
	RoutesPackage     string
	ScopePascal       string
	RoutesFunc        string
	PascalName        string
	CamelName         string
	SnakeName         string
	KebabName         string
	PluralSnake       string
	PluralPascal      string
	PluralKebab       string
	TableName         string
	TableAlias        string
	TagName           string
	ChineseName       string
	RoutePath         string
	IsAdmin           bool
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
	routesPackage := "route"
	routesPath := filepath.Join("internal", "route", moduleFile)
	if opts.IsAdmin {
		scopePrefix = "admin"
		dtoPackage = "admindto"
		handlerPackage = "adminhandler"
		servicePackage = "adminservice"
		routesPackage = "adminroute"
		routesPath = filepath.Join("internal", "route", "admin", moduleFile)
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

// Plan 描述任意脚手架生成规划契约。
type Plan interface {
	Name() string
	Files() ([]PlannedFile, error)
	CleanupDirs() []string
}

// CRUDPlan 实现针对分层优先（route → handler → service → dto）CRUD 模块的生成规划契约。
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
func (p *CRUDPlan) Name() string { return p.opts.Name }

// Data 返回模板上下文数据。
func (p *CRUDPlan) Data() TemplateData { return p.data }

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
		{RelPath: p.data.RoutesPath, Template: "route.tmpl", Data: p.data},
	}, nil
}

// CleanupDirs 返回该模块在销毁回退时需要尝试清理的目录列表。
func (p *CRUDPlan) CleanupDirs() []string {
	dtoDir, handlerDir, serviceDir := p.moduleDirs()
	return []string{dtoDir, handlerDir, serviceDir}
}

// Pipeline 提供可插拔的代码生成与回退执行管线引擎。
type Pipeline struct {
	TemplateFS fs.FS
	RootDir    string
	Force      bool
	DryRun     bool
}

// NewPipeline 构造带有默认配置的生成管线实例。
func NewPipeline(templateFS fs.FS, rootDir string, force, dryRun bool) *Pipeline {
	if templateFS == nil {
		templateFS = defaultTemplateFS()
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
	return NewPipeline(nil, opts.RootDir, opts.Force, opts.DryRun).Execute(NewCRUDPlan(opts))
}

// DestroyCRUD 回退并安全清理指定 CRUD 模块的生成文件与空目录。
func DestroyCRUD(opts GeneratorOptions) ([]GeneratedResult, error) {
	if strings.TrimSpace(opts.Name) == "" {
		return nil, errors.New("module name cannot be empty")
	}
	return NewPipeline(nil, opts.RootDir, opts.Force, opts.DryRun).Destroy(NewCRUDPlan(opts))
}

var (
	matchFirstCap = regexp.MustCompile("(.)([A-Z][a-z]+)")
	matchAllCap   = regexp.MustCompile("([a-z0-9])([A-Z])")
)

// ToSnakeCase 把 CamelCase / PascalCase / kebab-case / 空格分隔的字符串转为 snake_case。
func ToSnakeCase(str string) string {
	s := strings.TrimSpace(str)
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	snake := matchFirstCap.ReplaceAllString(s, "${1}_${2}")
	snake = matchAllCap.ReplaceAllString(snake, "${1}_${2}")
	return strings.ToLower(snake)
}

// ToKebabCase 把字符串转为 kebab-case。
func ToKebabCase(str string) string {
	return strings.ReplaceAll(ToSnakeCase(str), "_", "-")
}

// ToPascalCase 把 snake_case / kebab-case / 空格分隔的字符串转为 PascalCase。
func ToPascalCase(str string) string {
	var sb strings.Builder
	for part := range strings.SplitSeq(ToSnakeCase(str), "_") {
		if word := strings.TrimSpace(part); word != "" {
			sb.WriteString(strings.ToUpper(word[:1]))
			sb.WriteString(strings.ToLower(word[1:]))
		}
	}
	return sb.String()
}

// ToCamelCase 把字符串转为 camelCase。
func ToCamelCase(str string) string {
	pascal := ToPascalCase(str)
	if pascal == "" {
		return ""
	}
	return strings.ToLower(pascal[:1]) + pascal[1:]
}

// Pluralize 为模型/资源名词生成务实可用的英文复数形式。
func Pluralize(noun string) string {
	s := strings.TrimSpace(noun)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	lastChar := lower[len(lower)-1]

	switch lower {
	case "person":
		return matchCase(s, "people")
	case "child":
		return matchCase(s, "children")
	case "man":
		return matchCase(s, "men")
	case "woman":
		return matchCase(s, "women")
	}

	if lastChar == 'y' && len(lower) > 1 && !isVowel(lower[len(lower)-2]) {
		return s[:len(s)-1] + "ies"
	}

	if strings.HasSuffix(lower, "s") || strings.HasSuffix(lower, "sh") ||
		strings.HasSuffix(lower, "ch") || strings.HasSuffix(lower, "x") ||
		strings.HasSuffix(lower, "z") {
		return s + "es"
	}

	return s + "s"
}

// Singularize 为模型/资源名词生成务实可用的英文单数形式。
func Singularize(noun string) string {
	s := strings.TrimSpace(noun)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)

	switch lower {
	case "people":
		return matchCase(s, "person")
	case "children":
		return matchCase(s, "child")
	case "men":
		return matchCase(s, "man")
	case "women":
		return matchCase(s, "woman")
	}

	if strings.HasSuffix(lower, "ies") && len(lower) > 3 {
		return s[:len(s)-3] + "y"
	}
	if strings.HasSuffix(lower, "ses") || strings.HasSuffix(lower, "shes") ||
		strings.HasSuffix(lower, "ches") || strings.HasSuffix(lower, "xes") ||
		strings.HasSuffix(lower, "zes") {
		return s[:len(s)-2]
	}
	if strings.HasSuffix(lower, "s") && !strings.HasSuffix(lower, "ss") && len(lower) > 1 {
		return s[:len(s)-1]
	}

	return s
}

func isVowel(c byte) bool {
	return c == 'a' || c == 'e' || c == 'i' || c == 'o' || c == 'u'
}

func matchCase(source, target string) string {
	if source == "" || target == "" {
		return target
	}
	if unicode.IsUpper(rune(source[0])) {
		return strings.ToUpper(target[:1]) + target[1:]
	}
	return strings.ToLower(target)
}
