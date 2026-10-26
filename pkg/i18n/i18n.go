// Package i18n 装载语言包，并负责语言协商与文案渲染。
//
// 语言资源的解析、分节合并、语言协商与模板渲染全部委托给 go-i18n（gin-contrib/i18n 的底层实现），
// 本包只保留项目需要的轻量门面：分节寻址、缺键回退与字段标签回退。
package i18n

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"unicode"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	i18ntemplate "github.com/nicksnyder/go-i18n/v2/i18n/template"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/text/language"

	"cgin/internal/config"
	"cgin/message"
)

// 语言包内的分节名，与 message/<语言>/<分节>.toml 一一对应。
const (
	sectionValidation = "validation"
	sectionFields     = "fields"
	sectionMail       = "mail"
	sectionInvoice    = "invoice"
)

// messageParser 让模板缺失变量渲染为空串，而不是把 "<no value>" 泄漏进用户可见文案。
var messageParser = &i18ntemplate.TextParser{Option: "missingkey=zero"}

// Translator 定义多语言协商与文本渲染的标准契约，遵循接口隔离与依赖倒置原则。
type Translator interface {
	// Fallback 返回配置的兜底默认语言标识。
	Fallback() string
	// Language 从 Accept-Language 协商出可用语言，无法匹配时回退默认语言。
	Language(acceptLanguage string) string
	// ValidationMessage 渲染一条校验失败文案；键或语言缺失时回退默认语言。
	ValidationMessage(language, key, field, param string) string
	// Mail 渲染一封邮件的主题与正文；共用一个 <key>_subject / <key>_body 的键对。
	Mail(language, key string, vars map[string]string) (subject, body string)
	// Invoice 渲染发票与结算凭据的展示文案；键或语言缺失时回退默认语言。
	Invoice(language, key string, vars map[string]string) string
	// FieldLabel 返回字段路径对应的可读标签，缺键时支持末级字段名回退，仍缺失时返回空串交由调用方兜底。
	FieldLabel(language, field string) string
}

// translator 按请求语言渲染文案；缺失的键一律回退到默认语言。
type translator struct {
	fallback  string
	languages []string
	matcher   language.Matcher
	bundle    *goi18n.Bundle
	// localizers 预置全部已知语言的查找器，只读共享，避免并发访问时加锁。
	localizers map[string]*goi18n.Localizer
}

var _ Translator = (*translator)(nil)

// NewWithFS 遵循依赖倒置原则，从指定的文件系统装载全部语言包。
func NewWithFS(defaultLanguage string, fsys fs.FS) (Translator, error) {
	loader := Loader{FS: fsys}

	languages, err := loader.Languages()
	if err != nil {
		return nil, err
	}
	if len(languages) == 0 {
		return nil, errors.New("i18n: no language bundle found")
	}
	if !slices.Contains(languages, defaultLanguage) {
		return nil, fmt.Errorf("i18n: default language %q bundle not found", defaultLanguage)
	}

	bundle := goi18n.NewBundle(language.Make(defaultLanguage))
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	t := &translator{
		fallback:   defaultLanguage,
		languages:  languages,
		bundle:     bundle,
		localizers: make(map[string]*goi18n.Localizer, len(languages)),
	}

	tags := make([]language.Tag, 0, len(languages))
	for _, lang := range languages {
		raw, readErr := loader.LoadMessage(lang + ".toml")
		if readErr != nil {
			return nil, readErr
		}
		if _, parseErr := bundle.ParseMessageFileBytes(raw, lang+".toml"); parseErr != nil {
			return nil, fmt.Errorf("i18n: parse language bundle %q failed: %w", lang, parseErr)
		}

		tags = append(tags, language.Make(lang))
		t.localizers[lang] = newLocalizer(bundle, lang, defaultLanguage)
	}

	t.matcher = language.NewMatcher(tags)

	return t, nil
}

// New 装载内嵌的全部语言包，保持现有配置签名兼容。
func New(cfg *config.Config) (Translator, error) {
	return NewWithFS(cfg.I18n.DefaultLanguage, message.FS)
}

// newLocalizer 构造带默认语言回退的查找器，语言与默认语言相同时只保留一个标签。
func newLocalizer(bundle *goi18n.Bundle, lang, fallback string) *goi18n.Localizer {
	if lang == fallback {
		return goi18n.NewLocalizer(bundle, lang)
	}
	return goi18n.NewLocalizer(bundle, lang, fallback)
}

// Fallback 返回配置的兜底默认语言标识。
func (t *translator) Fallback() string {
	if t == nil {
		return "en"
	}
	return t.fallback
}

// Language 从 Accept-Language 协商出可用语言，无法匹配时回退默认语言。
// 按优先级逐个候选取值尝试，谁先命中就用谁。
func (t *translator) Language(acceptLanguage string) string {
	if t == nil {
		return "en"
	}

	for part := range strings.SplitSeq(acceptLanguage, ",") {
		tag, _, _ := strings.Cut(part, ";")
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}

		_, index, confidence := t.matcher.Match(language.Make(tag))
		if confidence == language.No {
			continue
		}
		return t.languages[index]
	}
	return t.fallback
}

// ValidationMessage 渲染一条校验失败文案；键或语言缺失时回退默认语言。
func (t *translator) ValidationMessage(language, key, field, param string) string {
	if t == nil {
		return ""
	}

	label := cmp.Or(t.FieldLabel(language, field), humanize(field))
	return t.localize(language, sectionValidation, key, map[string]string{"Field": label, "Param": param})
}

// Mail 渲染一封邮件的主题与正文；共用一个 <key>_subject / <key>_body 的键对。
func (t *translator) Mail(language, key string, vars map[string]string) (subject, body string) {
	return t.localize(language, sectionMail, key+"_subject", vars),
		t.localize(language, sectionMail, key+"_body", vars)
}

// Invoice 渲染发票与结算凭据的展示文案；键或语言缺失时回退默认语言。
func (t *translator) Invoice(language, key string, vars map[string]string) string {
	return t.localize(language, sectionInvoice, key, vars)
}

// FieldLabel 返回字段路径对应的可读标签，缺键时支持末级字段名回退，仍缺失时返回空串交由调用方兜底。
func (t *translator) FieldLabel(language, field string) string {
	if t == nil {
		return ""
	}

	if label := t.localize(language, sectionFields, field, nil); label != "" {
		return label
	}

	// 支持路径解构回退（例如 items.0.email 或 user.avatar 回退查找 email 或 avatar）
	if _, last, found := strings.CutLast(field, "."); found && last != "" {
		if _, err := strconv.Atoi(last); err != nil {
			return t.localize(language, sectionFields, last, nil)
		}
	}
	return ""
}

// localize 按 "section.key" 组装消息 ID 并交给 go-i18n 渲染。
//
// 查找器自带默认语言回退，因此缺键会顺次落到默认语言；两者都缺时 Localize 返回错误，
// 这里统一收敛为空串，保持"缺键返回空"的既有契约。
func (t *translator) localize(lang, section, key string, data map[string]string) string {
	if section == "" || key == "" {
		return ""
	}
	if data == nil {
		data = map[string]string{}
	}

	rendered, err := t.localizer(lang).Localize(&goi18n.LocalizeConfig{
		MessageID:      section + "." + key,
		TemplateData:   data,
		TemplateParser: messageParser,
	})
	if err != nil {
		return ""
	}
	return rendered
}

// localizer 返回指定语言的查找器；未知语言按需构造且不缓存，避免缓存无界增长。
func (t *translator) localizer(lang string) *goi18n.Localizer {
	if l, ok := t.localizers[lang]; ok {
		return l
	}
	return newLocalizer(t.bundle, lang, t.fallback)
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
