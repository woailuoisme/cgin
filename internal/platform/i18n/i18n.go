// Package i18n 装载语言包，并负责语言协商与文案渲染。
package i18n

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/pelletier/go-toml/v2"
	"github.com/samber/lo"

	"bgin/internal/config"
	"bgin/lang"
)

// 语言包内的分节名，与 lang/<语言>/<分节>.toml 一一对应。
const (
	sectionValidation = "validation"
	sectionFields     = "fields"
	sectionMail       = "mail"
	sectionInvoice    = "invoice"
)

// bundle 是一门语言的文案集合，动态管理任意分节命名空间，遵循开闭原则。
type bundle struct {
	sections map[string]map[string]string
}

// Translator 按请求语言渲染文案；缺失的键一律回退到默认语言。
type Translator struct {
	fallback string
	bundles  map[string]*bundle
}

// NewWithFS 遵循依赖倒置原则，从指定的文件系统装载全部语言包。
func NewWithFS(defaultLanguage string, fsys fs.FS) (*Translator, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("i18n: read language directory failed: %w", err)
	}

	bundles := make(map[string]*bundle, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		loaded, err := loadBundle(fsys, entry.Name())
		if err != nil {
			return nil, err
		}
		bundles[entry.Name()] = loaded
	}
	if len(bundles) == 0 {
		return nil, errors.New("i18n: no language bundle found")
	}

	if _, ok := bundles[defaultLanguage]; !ok {
		return nil, fmt.Errorf("i18n: default language %q bundle not found", defaultLanguage)
	}
	return &Translator{fallback: defaultLanguage, bundles: bundles}, nil
}

// New 装载内嵌的全部语言包，保持现有配置签名兼容。
func New(cfg *config.Config) (*Translator, error) {
	return NewWithFS(cfg.I18n.DefaultLanguage, lang.FS)
}

var (
	defaultMu         sync.RWMutex
	defaultTranslator *Translator
)

// SetDefault 注册全局默认翻译器。
func SetDefault(translator *Translator) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultTranslator = translator
}

// Default 返回全局默认翻译器；未注册时为 nil，调用方须自行判断。
func Default() *Translator {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultTranslator
}

// loadBundle 动态遍历并读取单门语言目录下的全部 TOML 文案文件。
func loadBundle(fsys fs.FS, langDir string) (*bundle, error) {
	entries, err := fs.ReadDir(fsys, langDir)
	if err != nil {
		return nil, fmt.Errorf("i18n: read language directory %s failed: %w", langDir, err)
	}

	loaded := &bundle{
		sections: make(map[string]map[string]string, len(entries)),
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		section := strings.TrimSuffix(entry.Name(), ".toml")
		target := make(map[string]string)
		filePath := langDir + "/" + entry.Name()
		if err := decode(fsys, filePath, &target); err != nil {
			return nil, err
		}
		loaded.sections[section] = target
	}
	return loaded, nil
}

// decode 读取并解析一个 TOML 资源文件。
func decode(fsys fs.FS, name string, dst any) error {
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return fmt.Errorf("i18n: read %s failed: %w", name, err)
	}
	if err := toml.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("i18n: decode %s failed: %w", name, err)
	}
	return nil
}

// Language 从 Accept-Language 协商出可用语言，无法匹配时回退默认语言。
// 按优先级逐个候选取值尝试，谁先命中就用谁。
func (t *Translator) Language(acceptLanguage string) string {
	for part := range strings.SplitSeq(acceptLanguage, ",") {
		tag, _, _ := strings.Cut(part, ";")
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if matched := t.match(tag); matched != "" {
			return matched
		}
	}
	return t.fallback
}

// match 依次尝试精确匹配、逐级去掉子标签，最后按主语言前缀匹配已有语言包。
// 因此 en-GB 命中 en，zh-Hans-CN 命中 zh-CN。
func (t *Translator) match(tag string) string {
	for candidate := tag; candidate != ""; {
		if _, ok := t.bundles[candidate]; ok {
			return candidate
		}

		prefix, _, found := strings.CutLast(candidate, "-")
		if !found {
			break
		}
		candidate = prefix
	}

	primary, _, _ := strings.Cut(tag, "-")
	for _, name := range t.sortedNames() {
		if name == primary || strings.HasPrefix(name, primary+"-") {
			return name
		}
	}
	return ""
}

// sortedNames 返回排序后的语言包名，保证前缀匹配的结果稳定可预期。
func (t *Translator) sortedNames() []string {
	return slices.Sorted(maps.Keys(t.bundles))
}

// ValidationMessage 渲染一条校验失败文案；键或语言缺失时回退默认语言。
func (t *Translator) ValidationMessage(language, key, field, param string) string {
	tpl := t.template(language, sectionValidation, key)
	if tpl == "" {
		return ""
	}

	label := cmp.Or(t.FieldLabel(language, field), humanize(field))
	return render(tpl, map[string]string{"Field": label, "Param": param})
}

// Mail 渲染一封邮件的主题与正文；共用一个 <key>_subject / <key>_body 的键对。
func (t *Translator) Mail(language, key string, vars map[string]string) (subject, body string) {
	subject = render(t.template(language, sectionMail, key+"_subject"), vars)
	body = render(t.template(language, sectionMail, key+"_body"), vars)
	return subject, body
}

// Invoice 渲染发票与结算凭据的展示文案；键或语言缺失时回退默认语言。
func (t *Translator) Invoice(language, key string, vars map[string]string) string {
	return render(t.template(language, sectionInvoice, key), vars)
}

// T 统一通用翻译契约：支持 "section.key" 路径语法（例如 "auth.login_failed" 或 "validation.required"）。
// 支持传入可选变量映射 vars 进行 {{.Key}} 占位符动态替换；缺键时返回 key 自身。
func (t *Translator) T(language, key string, vars ...map[string]string) string {
	section, subKey, found := strings.Cut(key, ".")
	if !found {
		section, subKey = "", key
	}

	tpl := lo.Ternary(section != "", t.template(language, section, subKey), t.findAnySection(language, subKey))
	if tpl == "" {
		return ""
	}

	if v := lo.FirstOrEmpty(vars); v != nil {
		return render(tpl, v)
	}
	return tpl
}

func (t *Translator) findAnySection(language, key string) string {
	for _, lang := range []string{language, t.fallback} {
		if b, ok := t.bundles[lang]; ok {
			for _, sec := range b.sections {
				if val, exists := sec[key]; exists {
					return val
				}
			}
		}
	}
	return ""
}

// FieldLabel 返回字段路径对应的可读标签，缺键时支持末级字段名回退，仍缺失时返回空串交由调用方兜底。
func (t *Translator) FieldLabel(language, field string) string {
	if label := t.template(language, sectionFields, field); label != "" {
		return label
	}

	// 支持路径解构回退（例如 items.0.email 或 user.avatar 回退查找 email 或 avatar）
	if _, last, found := strings.CutLast(field, "."); found && last != "" {
		if _, err := strconv.Atoi(last); err != nil {
			return t.template(language, sectionFields, last)
		}
	}
	return ""
}

// template 按语言与分节取模板，缺键时用默认语言兜底。
func (t *Translator) template(language, section, key string) string {
	return cmp.Or(lookup(t.bundles[language], section, key), lookup(t.bundles[t.fallback], section, key))
}

// lookup 从语言包中取出指定分节的模板。
func lookup(b *bundle, section, key string) string {
	if b == nil || b.sections == nil {
		return ""
	}
	if sec, ok := b.sections[section]; ok {
		return sec[key]
	}
	return ""
}

// render 替换模板中的 {{.Key}} 占位符。
func render(tpl string, vars map[string]string) string {
	if tpl == "" || len(vars) == 0 {
		return tpl
	}

	pairs := make([]string, 0, len(vars)*2)
	for key, value := range vars {
		pairs = append(pairs, "{{."+key+"}}", value)
	}
	return strings.NewReplacer(pairs...).Replace(tpl)
}

// humanize 把字段路径的最后一段（camelCase 或 snake_case）转成空格分隔的小写短语。
func humanize(field string) string {
	_, segment, found := strings.CutLast(field, ".")
	if !found {
		segment = field
	}
	if segment == "" {
		return field
	}

	var b strings.Builder
	b.Grow(len(segment) + 4)
	for i, r := range segment {
		if r == '_' {
			b.WriteByte(' ')
			continue
		}
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
