// Package sms 提供短消息通知渠道与多厂商驱动实现。
package sms

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/samber/lo"
)

// Message 表示标准的短信通知载荷。
type Message struct {
	PhoneNumber     string
	SignName        string
	TemplateCode    string
	TemplateParam   map[string]string
	Content         string
	DriverTemplates map[string]string
	DriverSigns     map[string]string
	ParamSet        []string
	ParamOrder      []string
}

// NewMessage 创建初始化为空的短信消息对象。
func NewMessage() *Message {
	return &Message{
		TemplateParam:   make(map[string]string),
		DriverTemplates: make(map[string]string),
		DriverSigns:     make(map[string]string),
	}
}

// WithPhone 设置接收手机号。
func (m *Message) WithPhone(phone string) *Message {
	m.PhoneNumber = phone
	return m
}

// WithSignName 设置通用短信签名。
func (m *Message) WithSignName(sign string) *Message {
	m.SignName = sign
	return m
}

// WithTemplate 设置通用模板 ID 或模板编码。
func (m *Message) WithTemplate(code string) *Message {
	m.TemplateCode = code
	return m
}

// WithParam 设置单项模板变量。
func (m *Message) WithParam(key, val string) *Message {
	if m.TemplateParam == nil {
		m.TemplateParam = make(map[string]string)
	}
	m.TemplateParam[key] = val
	return m
}

// WithParams 批量设置模板变量。
func (m *Message) WithParams(params map[string]string) *Message {
	if m.TemplateParam == nil {
		m.TemplateParam = make(map[string]string)
	}
	maps.Copy(m.TemplateParam, params)
	return m
}

// WithParamSet 设置有序模板变量列表（适用于腾讯云等位置参数驱动）。
// 采用防御性拷贝隔离输入切片，遵循 Uber Go 边界规范。
func (m *Message) WithParamSet(paramSet ...string) *Message {
	m.ParamSet = slices.Clone(paramSet)
	return m
}

// WithParamOrder 设置从 TemplateParam 提取有序切片的键名优先顺序。
// 采用防御性拷贝隔离输入切片，遵循 Uber Go 边界规范。
func (m *Message) WithParamOrder(keys ...string) *Message {
	m.ParamOrder = slices.Clone(keys)
	return m
}

// WithContent 设置纯文本短信正文。
func (m *Message) WithContent(content string) *Message {
	m.Content = content
	return m
}

// WithDriverTemplate 为指定驱动配置专属模板标识。
func (m *Message) WithDriverTemplate(driver, template string) *Message {
	if m.DriverTemplates == nil {
		m.DriverTemplates = make(map[string]string)
	}
	m.DriverTemplates[driver] = template
	return m
}

// WithDriverSign 为指定驱动配置专属签名。
func (m *Message) WithDriverSign(driver, sign string) *Message {
	if m.DriverSigns == nil {
		m.DriverSigns = make(map[string]string)
	}
	m.DriverSigns[driver] = sign
	return m
}

// WithAliyunTemplate 针对阿里云短信配置专属模板 Code 及可选参数。
func (m *Message) WithAliyunTemplate(code string, params ...map[string]string) *Message {
	m.WithDriverTemplate("aliyun", code)
	if len(params) > 0 && params[0] != nil {
		m.WithParams(params[0])
	}
	return m
}

// WithTencentTemplate 针对腾讯云短信配置专属模板 ID 及有序参数切片。
func (m *Message) WithTencentTemplate(templateID string, paramSet ...string) *Message {
	m.WithDriverTemplate("tencent", templateID)
	if len(paramSet) > 0 {
		m.WithParamSet(paramSet...)
	}
	return m
}

// WithSMSBaoTemplate 针对短信宝配置专属文本模板（支持 {key} 与 ${key} 占位符）。
func (m *Message) WithSMSBaoTemplate(template string) *Message {
	m.WithDriverTemplate("smsbao", template)
	return m
}

// ResolveTemplate 获取指定驱动对应的模板标识（优先级：厂商专属 -> 通用设置 -> 兜底默认值）。
func (m *Message) ResolveTemplate(driver string, fallback ...string) string {
	if m.DriverTemplates != nil {
		if t, ok := m.DriverTemplates[driver]; ok && t != "" {
			return t
		}
	}
	if m.TemplateCode != "" {
		return m.TemplateCode
	}
	for _, fb := range fallback {
		if fb != "" {
			return fb
		}
	}
	return ""
}

// ResolveSign 获取指定驱动对应的签名（优先级：厂商专属 -> 通用设置 -> 兜底默认值）。
func (m *Message) ResolveSign(driver string, fallback ...string) string {
	if m.DriverSigns != nil {
		if s, ok := m.DriverSigns[driver]; ok && s != "" {
			return s
		}
	}
	if m.SignName != "" {
		return m.SignName
	}
	for _, fb := range fallback {
		if fb != "" {
			return fb
		}
	}
	return ""
}

// extractSingleParam 尝试从参数字典提取单个验证码变量。
func extractSingleParam(params map[string]string) ([]string, bool) {
	if len(params) == 1 {
		for _, val := range params {
			return []string{val}, true
		}
	}
	if code := cmp.Or(params["code"], params["otp"]); code != "" && len(params) <= 2 {
		if len(params) == 1 || (len(params) == 2 && params["code"] == params["otp"]) {
			return []string{code}, true
		}
	}
	return nil, false
}

// extractNumericParams 尝试将全数字 Key 按整数大小升序提取为参数切片。
func extractNumericParams(params map[string]string) ([]string, bool) {
	type numPair struct {
		idx int
		val string
	}
	pairs := make([]numPair, 0, len(params))
	for k, v := range params {
		n, err := strconv.Atoi(k)
		if err != nil || n < 0 {
			return nil, false
		}
		pairs = append(pairs, numPair{idx: n, val: v})
	}
	if len(pairs) == 0 {
		return nil, false
	}

	slices.SortFunc(pairs, func(a, b numPair) int {
		return cmp.Compare(a.idx, b.idx)
	})
	return lo.Map(pairs, func(p numPair, _ int) string {
		return p.val
	}), true
}

// ResolveTencentParamSet 智能自适应推断腾讯云短信有序参数切片。
func (m *Message) ResolveTencentParamSet() []string {
	// 1. 若显式设置了 ParamSet，直接返回
	if len(m.ParamSet) > 0 {
		return slices.Clone(m.ParamSet)
	}

	if len(m.TemplateParam) == 0 {
		return nil
	}

	// 2. 若配置了 ParamOrder，按指定键顺序提取
	if len(m.ParamOrder) > 0 {
		var set []string
		for _, key := range m.ParamOrder {
			if val, ok := m.TemplateParam[key]; ok {
				set = append(set, val)
			}
		}
		if len(set) > 0 {
			return set
		}
	}

	// 3. 单参数快捷处理：仅有一个键或只包含常见验证码键 code/otp
	if set, ok := extractSingleParam(m.TemplateParam); ok {
		return set
	}

	// 4. 数字键检测：若所有 Key 均为合法非负整数，按数字升序提取
	if set, ok := extractNumericParams(m.TemplateParam); ok {
		return set
	}

	// 5. 字典序兜底提取
	keys := slices.Sorted(maps.Keys(m.TemplateParam))
	return lo.Map(keys, func(k string, _ int) string {
		return m.TemplateParam[k]
	})
}

// RenderContent 将模板字符串（支持 {key} 与 ${key} 占位符）与当前变量进行插值替换。
func (m *Message) RenderContent(template string, sign string) string {
	if template == "" {
		return ""
	}

	res := template
	// 替换签名变量（优先匹配长前缀 ${sign}）
	if sign != "" {
		res = strings.ReplaceAll(res, "${sign}", sign)
		res = strings.ReplaceAll(res, "{sign}", sign)
	}

	// 替换命名参数（优先匹配长前缀 ${key}）
	for k, v := range m.TemplateParam {
		res = strings.ReplaceAll(res, "${"+k+"}", v)
		res = strings.ReplaceAll(res, "{"+k+"}", v)
	}

	// 替换位置参数（若存在，优先匹配长前缀 ${idx}）
	paramSet := m.ResolveTencentParamSet()
	for i, val := range paramSet {
		idxStr := strconv.Itoa(i + 1)
		res = strings.ReplaceAll(res, "${"+idxStr+"}", val)
		res = strings.ReplaceAll(res, "{"+idxStr+"}", val)
	}

	return res
}

// Notification 声明支持短信格式化的通知接口契约。
type Notification interface {
	ToSMS(notifiable any) any
}
