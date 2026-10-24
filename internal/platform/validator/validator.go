// Package validator 封装基于 go-playground/validator 的请求数据校验与国际化错误转换。
package validator

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/aarondl/opt/omitnull"
	"github.com/dustin/go-humanize"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	govalidator "github.com/go-playground/validator/v10"
	"github.com/samber/lo"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/i18n"
)

func init() {
	if err := InitEngine(); err != nil {
		panic(err)
	}
}

// Validatable 是支持 DTO 自校验与跨字段复合校验的接口契约。
type Validatable interface {
	Validate() error
}

// customValidator 包装 Gin 默认 StructValidator，在完成标签校验后自动回调 Validatable 接口。
type customValidator struct {
	base   binding.StructValidator
	engine *govalidator.Validate
}

func (cv *customValidator) ValidateStruct(obj any) error {
	if cv.base != nil {
		if err := cv.base.ValidateStruct(obj); err != nil {
			return err
		}
	}
	return checkValidatable(obj)
}

func (cv *customValidator) Engine() any {
	if cv.engine != nil {
		return cv.engine
	}
	if cv.base != nil {
		return cv.base.Engine()
	}
	return nil
}

// checkValidatable 递归检查对象（或其指针/切片元素）是否实现 Validatable 接口并触发。
func checkValidatable(obj any) error {
	if obj == nil {
		return nil
	}
	if v, ok := obj.(Validatable); ok {
		return wrapValidationError(v.Validate())
	}

	val := reflect.ValueOf(obj)
	switch val.Kind() {
	case reflect.Pointer:
		if !val.IsNil() {
			return checkValidatable(val.Elem().Interface())
		}
	case reflect.Slice, reflect.Array:
		for i := range val.Len() {
			item := val.Index(i)
			target := item.Interface()
			if item.CanAddr() {
				target = item.Addr().Interface()
			}
			if err := checkValidatable(target); err != nil {
				return err
			}
		}
	}
	return nil
}

func wrapValidationError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errs.As(err); ok {
		return err
	}
	return errs.New(errs.CodeValidationFailed, err.Error())
}

var (
	registerMu           sync.Mutex
	customTagMessageKeys sync.Map // tag -> messageKey string
	bindingTags          = [...]string{"json", "form", "uri"}
)

// InitEngine 将自定义校验规则与字段名提取函数注册至 Gin 全局 binding.Validator，并挂载 Validatable 钩子。
func InitEngine() error {
	registerMu.Lock()
	defer registerMu.Unlock()

	var eng *govalidator.Validate
	if cv, ok := binding.Validator.(*customValidator); ok {
		eng = cv.engine
	} else if v, ok := binding.Validator.Engine().(*govalidator.Validate); ok {
		eng = v
		binding.Validator = &customValidator{
			base:   binding.Validator,
			engine: eng,
		}
	}

	if eng != nil {
		return registerRules(eng)
	}
	return nil
}

// RegisterRule 注册自定义校验标签与对应函数，并可选指定文案映射键。
func RegisterRule(tag string, fn govalidator.Func, messageKey ...string) error {
	registerMu.Lock()
	defer registerMu.Unlock()

	if msgKey := lo.FirstOrEmpty(messageKey); msgKey != "" {
		customTagMessageKeys.Store(tag, msgKey)
	}
	if v, ok := binding.Validator.Engine().(*govalidator.Validate); ok {
		return v.RegisterValidation(tag, fn)
	}
	return nil
}

// RegisterCustomTypeFunc 为验证器引擎注册自定义类型的解包或值提取函数。
func RegisterCustomTypeFunc(fn govalidator.CustomTypeFunc, types ...any) {
	registerMu.Lock()
	defer registerMu.Unlock()

	if v, ok := binding.Validator.Engine().(*govalidator.Validate); ok {
		v.RegisterCustomTypeFunc(fn, types...)
	}
}

// RegisterOmitNullTypes 为指定具体类型的 omitnull.Val[T] 容器注册统一的三态解包函数。
func RegisterOmitNullTypes(types ...any) {
	RegisterCustomTypeFunc(UnpackOmitNull, types...)
}

// UnpackOmitNull 统一从 omitnull.Val[T] 容器提取具体值供验证器校验。
// 若字段为缺省 (Unset) 或显式置空 (Null)，返回 nil（由 omitempty 标签跳过基础校验规则）；
// 若包含具体有效值，则解包返回 T 驱动后续标量规则。
func UnpackOmitNull(field reflect.Value) any {
	if !field.IsValid() {
		return nil
	}
	if field.Kind() == reflect.Pointer {
		if field.IsNil() {
			return nil
		}
		field = field.Elem()
	}
	m := field.MethodByName("Get")
	if m.IsValid() {
		results := m.Call(nil)
		if len(results) == 2 && results[1].Bool() {
			return results[0].Interface()
		}
	}
	return nil
}

// registerRules 统一注册 json/form/uri 标签名解析与基础校验规则。
func registerRules(v *govalidator.Validate) error {
	v.SetTagName("validate")
	v.RegisterTagNameFunc(func(field reflect.StructField) string {
		for _, tag := range bindingTags {
			if val := field.Tag.Get(tag); val != "" {
				name, _, _ := strings.Cut(val, ",")
				if name != "" && name != "-" {
					return name
				}
			}
		}
		return field.Name
	})

	v.RegisterCustomTypeFunc(UnpackOmitNull,
		omitnull.Val[string]{},
		omitnull.Val[bool]{},
		omitnull.Val[int]{},
		omitnull.Val[int64]{},
		omitnull.Val[float64]{},
		omitnull.Val[time.Time]{},
	)

	rules := []struct {
		tag string
		fn  govalidator.Func
	}{
		{"usernameslug", validateUsernameSlug},
		{"e164", validateE164},
		{"notreserved", validateNotReserved},
		{"file", validateFile},
		{"image", validateImage},
		{"maxsize", validateMaxSize},
		{"mimes", validateMimes},
		{"password", validatePassword},
	}
	for _, rule := range rules {
		if err := v.RegisterValidation(rule.tag, rule.fn); err != nil {
			return fmt.Errorf("register validation %q: %w", rule.tag, err)
		}
	}
	return nil
}

// usernameSlugPattern 限定用户名只含小写字母、数字与下划线。
var usernameSlugPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// e164Pattern 限定手机号为 E.164 规范（加号开头，7 到 15 位数字）。
var e164Pattern = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

// reservedShortNames 是禁止占用的短名，避免与系统路径或保留字冲突。
var reservedShortNames = map[string]struct{}{
	"admin":  {},
	"root":   {},
	"system": {},
	"me":     {},
	"api":    {},
	"bgin":   {},
}

// validateUsernameSlug 校验用户名的字符集。前置长度守卫（1-64 字符），防御恶意超长串造成正则回溯。
func validateUsernameSlug(field govalidator.FieldLevel) bool {
	str := field.Field().String()
	return len(str) > 0 && len(str) <= 64 && usernameSlugPattern.MatchString(str)
}

// validateE164 校验手机号是否符合 E.164 规范。前置长度截断保护（8-20 字符）。
func validateE164(field govalidator.FieldLevel) bool {
	str := field.Field().String()
	return len(str) >= 8 && len(str) <= 20 && e164Pattern.MatchString(str)
}

// validateNotReserved 拒绝保留短名，大小写不敏感。
func validateNotReserved(field govalidator.FieldLevel) bool {
	_, reserved := reservedShortNames[strings.ToLower(field.Field().String())]
	return !reserved
}

// extractFileHeader 从 reflect.Value 提取有效的非空 *multipart.FileHeader 指针。
func extractFileHeader(val reflect.Value) (*multipart.FileHeader, bool) {
	if !val.IsValid() {
		return nil, false
	}
	if val.CanInterface() {
		switch file := val.Interface().(type) {
		case *multipart.FileHeader:
			return file, file != nil
		case multipart.FileHeader:
			return &file, true
		}
	}
	if val.Kind() == reflect.Pointer && !val.IsNil() {
		return extractFileHeader(val.Elem())
	}
	return nil, false
}

// validateFile 校验字段是否为有效的非空上传文件句柄。
func validateFile(field govalidator.FieldLevel) bool {
	file, ok := extractFileHeader(field.Field())
	return ok && file.Size > 0
}

// imageExtensions 白名单集合，支持常见 Web 图像格式。
var imageExtensions = map[string]struct{}{
	".jpg":  {},
	".jpeg": {},
	".png":  {},
	".webp": {},
	".gif":  {},
	".bmp":  {},
	".svg":  {},
}

// validateImage 校验上传文件是否为合法图像格式（扩展名白名单 + 首部魔数安全探测）。
func validateImage(field govalidator.FieldLevel) bool {
	file, ok := extractFileHeader(field.Field())
	if !ok || file.Size == 0 {
		return false
	}

	ext := strings.ToLower(filepath.Ext(file.Filename))
	if _, allowed := imageExtensions[ext]; !allowed {
		return false
	}

	src, err := file.Open()
	if err != nil {
		return false
	}
	defer func() {
		_ = src.Close()
	}()

	buf := make([]byte, 512)
	n, err := src.Read(buf)
	if (err != nil && !errors.Is(err, io.EOF)) || n == 0 {
		return false
	}

	mimeType := http.DetectContentType(buf[:n])
	return strings.HasPrefix(mimeType, "image/") ||
		(ext == ".svg" && (strings.Contains(mimeType, "xml") || strings.Contains(mimeType, "svg")))
}

// validateMaxSize 校验上传文件字节尺寸上限（支持 2M、500K、1048576 字节格式）。
func validateMaxSize(field govalidator.FieldLevel) bool {
	file, ok := extractFileHeader(field.Field())
	if !ok || field.Param() == "" {
		return false
	}

	limit, err := humanize.ParseBytes(field.Param())
	if err != nil {
		if limit, err = strconv.ParseUint(field.Param(), 10, 64); err != nil {
			return false
		}
	}
	return file.Size >= 0 && (limit > math.MaxInt64 || file.Size <= int64(limit))
}

// validateMimes 校验上传文件扩展名是否在指定的白名单列表中（例如 mimes=png jpg webp）。
func validateMimes(field govalidator.FieldLevel) bool {
	file, ok := extractFileHeader(field.Field())
	if !ok {
		return false
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file.Filename), "."))
	if ext == "" {
		return false
	}

	return slices.Contains(strings.Fields(strings.ToLower(field.Param())), ext)
}

// hasLetterAndDigit 检查字符串是否同时包含字母与数字。
func hasLetterAndDigit(s string) bool {
	return strings.ContainsFunc(s, unicode.IsLetter) && strings.ContainsFunc(s, unicode.IsDigit)
}

// validatePassword 校验口令复杂度：必须同时包含字母与数字（支持可选参数指定最小长度，默认 8 位）。
func validatePassword(field govalidator.FieldLevel) bool {
	str := field.Field().String()
	if str == "" {
		return true // 空值校验留给 required 规则
	}
	minLen := 8
	if n, err := strconv.Atoi(field.Param()); err == nil && n > 0 {
		minLen = n
	}
	return len(str) >= minLen && hasLetterAndDigit(str)
}

// PasswordStrength 校验口令是否同时含字母与数字，返回契约规定的 422 业务错误。
func PasswordStrength(field, password string) error {
	if hasLetterAndDigit(password) {
		return nil
	}
	return errs.New(errs.CodeAuthPasswordTooWeak, "The password does not meet strength requirements").
		WithField(field, "The "+field+" must contain both letters and numbers.")
}

// BindOptionalJSON 解析可省略的请求体：空体视为零值对象，非空体仍需是合法 JSON。
func BindOptionalJSON(c *gin.Context, target any) error {
	if c.Request.Body == nil || c.Request.ContentLength == 0 {
		return nil
	}
	if err := c.ShouldBindJSON(target); !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Translate 按请求语言渲染每条校验失败，键为 api.md 约定的字段路径。
func Translate(translator *i18n.Translator, language string, valErr govalidator.ValidationErrors) map[string][]string {
	fields := make(map[string][]string, len(valErr))
	for _, fe := range valErr {
		field := errs.FieldPath(fe)
		var message string
		if translator != nil {
			message = translator.ValidationMessage(language, MessageKey(fe), field, fe.Param())
		}
		fields[field] = append(fields[field], cmp.Or(message, fmt.Sprintf("The %s is invalid.", field)))
	}
	return fields
}

// MessageKey 选择文案键：优先查询自定义规则文案映射，随后区分数值与字符串的 min/max 措辞。
func MessageKey(fe govalidator.FieldError) string {
	tag := fe.Tag()
	if key, ok := customTagMessageKeys.Load(tag); ok {
		if str, isStr := key.(string); isStr {
			return str
		}
	}
	switch tag {
	case "min", "max", "gt", "gte", "lt", "lte":
		if isNumericKind(fe.Kind()) {
			return tag + "_numeric"
		}
	}
	return tag
}

// isNumericKind 判断字段是否为数值类型，用于挑选 min/max 的措辞。
func isNumericKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}
