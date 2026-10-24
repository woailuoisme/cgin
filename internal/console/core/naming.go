package core

import (
	"regexp"
	"strings"
	"unicode"
)

var matchFirstCap = regexp.MustCompile("(.)([A-Z][a-z]+)")
var matchAllCap = regexp.MustCompile("([a-z0-9])([A-Z])")

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
	words := splitWords(str)
	var sb strings.Builder
	for _, word := range words {
		if len(word) == 0 {
			continue
		}
		sb.WriteString(strings.ToUpper(word[:1]))
		sb.WriteString(strings.ToLower(word[1:]))
	}
	return sb.String()
}

// ToCamelCase 把字符串转为 camelCase。
func ToCamelCase(str string) string {
	pascal := ToPascalCase(str)
	if len(pascal) == 0 {
		return ""
	}
	return strings.ToLower(pascal[:1]) + pascal[1:]
}

// splitWords 按分隔符或驼峰边界把字符串切成小写单词。
func splitWords(s string) []string {
	snake := ToSnakeCase(s)
	var result []string
	for part := range strings.SplitSeq(snake, "_") {
		if p := strings.TrimSpace(part); p != "" {
			result = append(result, p)
		}
	}
	return result
}

// Pluralize 为模型/资源名词生成务实可用的英文复数形式。
func Pluralize(noun string) string {
	s := strings.TrimSpace(noun)
	if len(s) == 0 {
		return ""
	}
	lower := strings.ToLower(s)
	lastChar := lower[len(lower)-1]

	// 不规则与特殊形式
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

	// 辅音 + y：category -> categories
	if lastChar == 'y' && len(lower) > 1 && !isVowel(lower[len(lower)-2]) {
		return s[:len(s)-1] + "ies"
	}

	// s/sh/ch/x/z 结尾：bus -> buses, box -> boxes
	if strings.HasSuffix(lower, "s") || strings.HasSuffix(lower, "sh") ||
		strings.HasSuffix(lower, "ch") || strings.HasSuffix(lower, "x") ||
		strings.HasSuffix(lower, "z") {
		return s + "es"
	}

	// 默认加 s
	return s + "s"
}

// Singularize 为模型/资源名词生成务实可用的英文单数形式。
func Singularize(noun string) string {
	s := strings.TrimSpace(noun)
	if len(s) == 0 {
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
	if len(source) == 0 || len(target) == 0 {
		return target
	}
	if unicode.IsUpper(rune(source[0])) {
		return strings.ToUpper(target[:1]) + target[1:]
	}
	return strings.ToLower(target)
}
