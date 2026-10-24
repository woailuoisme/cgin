package errs

import (
	"strings"

	"github.com/go-playground/validator/v10"
)

// FieldPath 将 validator 字段命名空间规整为标准字段路径（如 tags.0.name）。
func FieldPath(fe validator.FieldError) string {
	namespace := fe.Namespace()
	if _, rest, found := strings.Cut(namespace, "."); found {
		namespace = rest
	}
	return NormalizePath(namespace)
}

// NormalizePath 将类似 a[0].b 形式的路径转换为 a.0.b。
func NormalizePath(namespace string) string {
	var b strings.Builder
	b.Grow(len(namespace))
	for i := range len(namespace) {
		switch namespace[i] {
		case '[':
			b.WriteByte('.')
		case ']':
			// 忽略右括号，索引数字已在前一次循环写入
		default:
			b.WriteByte(namespace[i])
		}
	}
	return b.String()
}

// rawValidationFields 未经翻译的校验错误兜底转换。
func rawValidationFields(valErr validator.ValidationErrors) map[string][]string {
	fields := make(map[string][]string, len(valErr))
	for _, fe := range valErr {
		key := FieldPath(fe)
		fields[key] = append(fields[key], fe.Tag())
	}
	return fields
}
