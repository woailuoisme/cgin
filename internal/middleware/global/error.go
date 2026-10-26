package global

import (
	"errors"

	gini18n "github.com/gin-contrib/i18n"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	govalidator "github.com/go-playground/validator/v10"

	"cgin/pkg/errs"
)

// ErrorHandler 把 c.Errors 渲染为统一 JSON 错误信封，并把校验明细翻译成请求语言。
// 官方无统一错误信封，属项目自建。
func (m *Middleware) ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		if len(c.Errors) == 0 || c.Writer.Written() {
			return
		}

		err := c.Errors.Last().Err
		if fields, ok := m.extractValidationFields(c, err); ok {
			err = errs.Validation(fields)
		}

		errs.Render(c, err)
	}
}

// resolveLanguage 取当前请求语言：优先 gin-contrib/i18n 的协商结果，其次 Translator 协商。
func (m *Middleware) resolveLanguage(c *gin.Context) string {
	if c.Value("i18n") != nil {
		return gini18n.GetCurrentLanguage(c).String()
	}

	lang := c.GetHeader("Accept-Language")
	if m.translator == nil {
		return lang
	}
	return m.translator.Language(lang)
}

// extractValidationFields 提取并翻译入参校验错误字典。
func (m *Middleware) extractValidationFields(c *gin.Context, err error) (map[string][]string, bool) {
	lang := m.resolveLanguage(c)

	if fields, ok := m.translateFieldKeys(lang, err); ok {
		return fields, true
	}

	if valErr, ok := errors.AsType[govalidator.ValidationErrors](err); ok {
		return m.translateValidation(lang, valErr), true
	}

	sliceErr, ok := errors.AsType[binding.SliceValidationError](err)
	if !ok {
		return nil, false
	}

	fields := make(map[string][]string)
	for _, subErr := range sliceErr {
		subValErr, subOk := errors.AsType[govalidator.ValidationErrors](subErr)
		if !subOk {
			continue
		}
		for k, v := range m.translateValidation(lang, subValErr) {
			fields[k] = append(fields[k], v...)
		}
	}
	return fields, len(fields) > 0
}

// translateValidation 渲染一条校验失败；未注入校验器时退回原始字段详情。
func (m *Middleware) translateValidation(lang string, valErr govalidator.ValidationErrors) map[string][]string {
	if m.validator != nil {
		return m.validator.Translate(m.translator, lang, valErr)
	}

	fields := make(map[string][]string, len(valErr))
	for _, fe := range valErr {
		field := errs.FieldPath(fe)
		fields[field] = append(fields[field], fe.Error())
	}
	return fields
}

// translateFieldKeys 将业务错误中基于键的字段明细渲染为指定语言文案。
func (m *Middleware) translateFieldKeys(lang string, err error) (map[string][]string, bool) {
	if m.translator == nil {
		return nil, false
	}

	bizErr, ok := errs.As(err)
	if !ok {
		return nil, false
	}

	keys := bizErr.FieldKeys()
	if len(keys) == 0 {
		return nil, false
	}

	fields := make(map[string][]string, len(keys))
	for field, messages := range keys {
		for _, message := range messages {
			rendered := m.translator.ValidationMessage(lang, message.Key, field, message.Param)
			if rendered != "" {
				fields[field] = append(fields[field], rendered)
			}
		}
	}
	return fields, len(fields) > 0
}
