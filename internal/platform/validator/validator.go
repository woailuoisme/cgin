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
	"time"
	"unicode"

	"github.com/aarondl/opt/omitnull"
	"github.com/dustin/go-humanize"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	govalidator "github.com/go-playground/validator/v10"

	"cgin/internal/platform/errs"
	"cgin/internal/platform/i18n"
)

// Validatable 是支持 DTO 自校验与跨字段复合校验的接口契约。
type Validatable interface {
	Validate() error
}

// Validator 持有校验引擎与自定义规则的文案键映射。
//
// 引擎只在本构造期被写入：构造完成后所有方法只读，因此可以安全并发共享。
// 规则不再通过 init() 自注册——谁注册、注册几次，由装配根显式决定。
type Validator struct {
	engine      *govalidator.Validate
	messageKeys map[string]string
}

// Option 描述构造期可追加的校验规则或自定义类型解包函数。
type Option func(*Validator) error

// WithRule 追加一条自定义校验标签，并可选指定其 i18n 文案键。
func WithRule(tag string, fn govalidator.Func, messageKey string) Option {
	return func(v *Validator) error {
		if err := v.engine.RegisterValidation(tag, fn); err != nil {
			return fmt.Errorf("validator: register rule %q: %w", tag, err)
		}
		if messageKey != "" {
			v.messageKeys[tag] = messageKey
		}
		return nil
	}
}

// WithOmitNullTypes 为具体的 omitnull.Val[T] 类型注册三态解包函数。
func WithOmitNullTypes(types ...any) Option {
	return func(v *Validator) error {
		v.engine.RegisterCustomTypeFunc(UnpackOmitNull, types...)
		return nil
	}
}

// New 构造校验器：先注册内置规则，再依次应用调用方追加的选项。
func New(opts ...Option) (*Validator, error) {
	v := &Validator{
		engine:      govalidator.New(),
		messageKeys: make(map[string]string, 8),
	}
	if err := registerRules(v.engine); err != nil {
		return nil, err
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(v); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// Install 把本实例挂载为 gin 的包级 StructValidator。
//
// gin 在 binding.Bind 内部调用它，这是框架提供的唯一校验入口；
// 挂载必须在服务开始接收请求之前完成，之后只读。
func (v *Validator) Install() {
	binding.Validator = &ginAdapter{validator: v}
}

// ginAdapter 把 Validator 适配为 gin 的 binding.StructValidator。
type ginAdapter struct {
	validator *Validator
}

func (a *ginAdapter) ValidateStruct(obj any) error { return a.validator.ValidateStruct(obj) }

func (a *ginAdapter) Engine() any { return a.validator.engine }

// ValidateStruct 先执行标签校验，再回调 Validatable 复合校验。
func (v *Validator) ValidateStruct(obj any) error {
	if v == nil || obj == nil {
		return nil
	}
	if err := v.engine.Struct(obj); err != nil {
		return err
	}
	return checkValidatable(obj)
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

// bindingTags 是字段名提取时依次尝试的结构体标签。
var bindingTags = [...]string{"json", "form", "uri"}

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
	"cgin":   {},
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
func (v *Validator) Translate(translator *i18n.Translator, language string, valErr govalidator.ValidationErrors) map[string][]string {
	fields := make(map[string][]string, len(valErr))
	for _, fe := range valErr {
		field := errs.FieldPath(fe)
		var message string
		if translator != nil {
			message = translator.ValidationMessage(language, v.MessageKey(fe), field, fe.Param())
		}
		fields[field] = append(fields[field], cmp.Or(message, fmt.Sprintf("The %s is invalid.", field)))
	}
	return fields
}

// MessageKey 选择文案键：优先查询自定义规则文案映射，随后区分数值与字符串的 min/max 措辞。
func (v *Validator) MessageKey(fe govalidator.FieldError) string {
	tag := fe.Tag()
	if key, ok := v.messageKeys[tag]; ok && key != "" {
		return key
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
