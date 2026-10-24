// Package i18n_test 验证语言协商与文案渲染（Q5 的双语 i18n 决策）。
package i18n_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/i18n"
)

// newTranslator 装载内嵌语言包，默认语言为 en。
func newTranslator(t *testing.T, defaultLanguage string) *i18n.Translator {
	t.Helper()

	translator, err := i18n.New(&config.Config{
		I18n: config.I18nConfig{DefaultLanguage: defaultLanguage},
	})
	require.NoError(t, err)
	return translator
}

// TestLanguageNegotiation 校验 Accept-Language 协商的优先级与回退。
func TestLanguageNegotiation(t *testing.T) {
	translator := newTranslator(t, "en")

	tests := []struct {
		name    string
		header  string
		want    string
		comment string
	}{
		{"按 q 值顺序取首个可用语言", "zh-CN,zh;q=0.9,en;q=0.8", "zh-CN", ""},
		{"区域子标签退到主语言", "en-GB,en;q=0.9", "en", ""},
		{"多级子标签按前缀命中语言包", "zh-Hans-CN", "zh-CN", ""},
		{"首个候选不可用时继续尝试", "fr-FR,zh-CN;q=0.8", "zh-CN", ""},
		{"全部不可用回退默认语言", "fr-FR,de;q=0.5", "en", ""},
		{"空请求头回退默认语言", "", "en", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, translator.Language(tt.header))
		})
	}
}

// TestFallbackLanguageIsConfigurable 校验默认语言可切换为 zh-CN。
func TestFallbackLanguageIsConfigurable(t *testing.T) {
	translator := newTranslator(t, "zh-CN")

	assert.Equal(t, "zh-CN", translator.Language("fr-FR"))
	assert.Equal(t, "en", translator.Language("en-US"))
}

// TestNewRejectsUnknownDefaultLanguage 校验默认语言必须在语言包中存在。
func TestNewRejectsUnknownDefaultLanguage(t *testing.T) {
	_, err := i18n.New(&config.Config{I18n: config.I18nConfig{DefaultLanguage: "fr"}})
	assert.Error(t, err)
}

// TestValidationMessageRendersLaravelStyle 校验 Laravel 风格措辞与占位符替换。
func TestValidationMessageRendersLaravelStyle(t *testing.T) {
	translator := newTranslator(t, "en")

	tests := []struct {
		name  string
		lang  string
		key   string
		field string
		param string
		want  string
	}{
		{"邮箱格式", "en", "email", "email", "", "The email address must be a valid email address."},
		{"字符串最小长度", "en", "min", "password", "8", "The password must be at least 8 characters."},
		{"已登记标签优先于自动拆词", "en", "min_numeric", "pageSize", "1", "The page size must be at least 1."},
		{"未登记字段自动拆成可读短语", "en", "min_numeric", "retryCount", "1", "The retry count must be at least 1."},
		{"必填", "en", "required", "name", "", "The name field is required."},
		{"保留字", "en", "notreserved", "username", "", "The username is reserved and cannot be used."},
		{"中文必填", "zh-CN", "required", "phoneNumber", "", "手机号为必填项。"},
		{"中文邮箱", "zh-CN", "email", "email", "", "邮箱地址必须是有效的邮箱地址。"},
		{"中文头像", "zh-CN", "image", "avatar", "", "头像必须是图片。"},
		{"嵌套路径回退标签", "zh-CN", "required", "user.email", "", "邮箱地址为必填项。"},
		{"切片嵌套回退标签", "zh-CN", "required", "items.0.avatar", "", "头像为必填项。"},
		{"口令规则英文", "en", "password", "password", "", "The password must contain both letters and numbers."},
		{"口令规则中文", "zh-CN", "password", "password", "", "密码必须同时包含字母和数字。"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, translator.ValidationMessage(tt.lang, tt.key, tt.field, tt.param))
		})
	}
}

// TestValidationMessageFallsBackThenGivesUp 校验缺键回退默认语言，未知规则返回空串。
func TestValidationMessageFallsBackThenGivesUp(t *testing.T) {
	translator := newTranslator(t, "en")

	assert.NotEmpty(t, translator.ValidationMessage("zh-CN", "required", "username", ""))
	assert.Empty(t, translator.ValidationMessage("en", "no_such_rule", "name", ""))
}

// TestHumanizeUnknownField 校验没有标签的字段名被拆成可读短语。
func TestHumanizeUnknownField(t *testing.T) {
	translator := newTranslator(t, "en")

	assert.Equal(t,
		"The totally unknown field must be a valid URL.",
		translator.ValidationMessage("en", "url", "totallyUnknownField", ""),
	)
}

// TestMailRendersSubjectAndBody 校验邮件主题与正文按语言渲染占位符。
func TestMailRendersSubjectAndBody(t *testing.T) {
	translator := newTranslator(t, "en")

	subject, body := translator.Mail("en", "reset_otp", map[string]string{
		"Name": "Alice", "Code": "123456", "TTL": "10",
	})
	assert.Equal(t, "Your bgin password reset code", subject)
	assert.Contains(t, body, "123456")
	assert.Contains(t, body, "10 minutes")

	zhSubject, zhBody := translator.Mail("zh-CN", "reset_otp", map[string]string{
		"Name": "小明", "Code": "654321", "TTL": "10",
	})
	assert.Equal(t, "你的 bgin 密码重置验证码", zhSubject)
	assert.Contains(t, zhBody, "654321")
}

// TestMailResetLinkCarriesLink 校验重置链接类邮件会带上链接占位符。
func TestMailResetLinkCarriesLink(t *testing.T) {
	translator := newTranslator(t, "en")

	_, body := translator.Mail("en", "reset_link", map[string]string{
		"Name": "Alice",
		"Link": "https://app.example.com/reset-password?token=abc&email=a%40b.com",
		"TTL":  "30",
	})
	assert.Contains(t, body, "https://app.example.com/reset-password?token=abc")
}

// TestMailMissingKeyReturnsEmpty 校验缺失模板键返回空串而非 panic 或半个模板。
func TestMailMissingKeyReturnsEmpty(t *testing.T) {
	translator := newTranslator(t, "en")

	subject, body := translator.Mail("en", "no_such_mail", nil)
	assert.Empty(t, subject)
	assert.Empty(t, body)
}

// TestFieldLabelReturnsEmptyForUnknown 校验未知字段名不返回伪造标签。
func TestFieldLabelReturnsEmptyForUnknown(t *testing.T) {
	translator := newTranslator(t, "en")

	assert.NotEmpty(t, translator.FieldLabel("en", "email"))
	assert.Empty(t, translator.FieldLabel("en", "notARegisteredField"))
}

// TestInvoiceRendersLocalizedLabels 校验发票模板文案按语言渲染，且占位符被替换。
//
// PDF 内文无法在测试里反读，所以"文案对不对"这一层只能在这里锁定；
// PDF 侧只断言渲染成功（见 tests/platform/pdf）。
func TestInvoiceRendersLocalizedLabels(t *testing.T) {
	translator := newTranslator(t, "en")

	tests := []struct {
		name     string
		lang     string
		key      string
		vars     map[string]string
		expected string
	}{
		{"中文标题", "zh-CN", "title", nil, "电子发票 / 结算凭据"},
		{"英文标题", "en", "title", nil, "Electronic Invoice / Statement"},
		{"中文凭据编号占位符", "zh-CN", "number", map[string]string{"Number": "INV-001"}, "凭据编号: INV-001"},
		{"英文凭据编号占位符", "en", "number", map[string]string{"Number": "INV-001"}, "Reference No.: INV-001"},
		{"中文备注占位符", "zh-CN", "notes", map[string]string{"Notes": "30 天内付款"}, "备注说明: 30 天内付款"},
		{"表头中文", "zh-CN", "item_description", nil, "项目与服务描述"},
		{"表头英文", "en", "unit_price", nil, "Unit Price"},
		{"签章中文", "zh-CN", "signature", nil, "财务专用章"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, translator.Invoice(tt.lang, tt.key, tt.vars))
		})
	}
}

// TestInvoiceFallsBackAndGivesUp 校验未知语言回退默认语言，未知键返回空串。
func TestInvoiceFallsBackAndGivesUp(t *testing.T) {
	translator := newTranslator(t, "en")

	assert.Equal(t, "Seller", translator.Invoice("fr-FR", "seller", nil),
		"未装载的语言应回退到默认语言，而不是留空标签")
	assert.NotEmpty(t, translator.Invoice("", "buyer", nil), "空语言标签同样回退默认语言")
	assert.Empty(t, translator.Invoice("en", "no_such_label", nil))
}

// TestTranslator_T 校验通用翻译方法 T 支持任意分节与变量替换。
func TestTranslator_T(t *testing.T) {
	translator := newTranslator(t, "en")

	// 直接访问 invoice 分节
	val := translator.T("zh-CN", "invoice.title", nil)
	assert.Equal(t, "电子发票 / 结算凭据", val)

	// 带参数替换
	ref := translator.T("zh-CN", "invoice.number", map[string]string{"Number": "INV-888"})
	assert.Equal(t, "凭据编号: INV-888", ref)

	// 语言回退到默认 en
	enVal := translator.T("de-DE", "invoice.title", nil)
	assert.Equal(t, "Electronic Invoice / Statement", enVal)

	// 不存在的键返回空
	assert.Empty(t, translator.T("en", "invoice.nonexistent_key", nil))
	assert.Empty(t, translator.T("en", "nonexistent_section.key", nil))
}

// TestNewWithFS 校验从自定义 fs.FS 载入翻译包。
func TestNewWithFS(t *testing.T) {
	mockFS := fstest.MapFS{
		"en/messages.toml": &fstest.MapFile{
			Data: []byte(`hello = "Hello, {{.Name}}!"`),
		},
		"zh-CN/messages.toml": &fstest.MapFile{
			Data: []byte(`hello = "你好，{{.Name}}！"`),
		},
	}

	translator, err := i18n.NewWithFS("en", mockFS)
	require.NoError(t, err)

	assert.Equal(t, "Hello, Bob!", translator.T("en", "messages.hello", map[string]string{"Name": "Bob"}))
	assert.Equal(t, "你好，张三！", translator.T("zh-CN", "messages.hello", map[string]string{"Name": "张三"}))
	assert.Equal(t, "Hello, Bob!", translator.T("fr-FR", "messages.hello", map[string]string{"Name": "Bob"}))
}
