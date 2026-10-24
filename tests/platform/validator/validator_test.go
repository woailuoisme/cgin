// Package validator_test 验证请求绑定与校验的外部契约（api.md 2.4 与 5.2）。
package validator_test

import (
	"bytes"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aarondl/opt/omitnull"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	govalidator "github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/i18n"
	"bgin/internal/platform/validator"
)

func init() { gin.SetMode(gin.TestMode) }

// signupPayload 覆盖本项目用到的全部自定义规则。
type signupPayload struct {
	Email       string  `json:"email" validate:"required,email"`
	Password    string  `json:"password" validate:"required,min=8"`
	Username    *string `json:"username" validate:"omitempty,min=3,usernameslug,notreserved"`
	PhoneNumber *string `json:"phoneNumber" validate:"omitempty,e164"`
}

// optionalBody 覆盖可省略请求体的行为。
type optionalBody struct {
	RefreshToken *string `json:"refreshToken" validate:"omitempty"`
}

// numericProbe 覆盖 min/max 在数值与字符串上的措辞分流。
type numericProbe struct {
	Count int    `json:"count" validate:"min=1"`
	Name  string `json:"name" validate:"min=1"`
}

// newTranslator 装载指定默认语言并构造 Translator。
func newTranslator(t *testing.T, defaultLanguage string) *i18n.Translator {
	t.Helper()

	translator, err := i18n.New(&config.Config{
		I18n: config.I18nConfig{DefaultLanguage: defaultLanguage},
	})
	require.NoError(t, err)
	return translator
}

// newContext 构造带请求体的 gin 上下文。
func newContext(body string) *gin.Context {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return ctx
}

// bindJSON 模拟 Gin 原生 ShouldBindJSON 并协同 translator 进行字段翻译。
func bindJSON(t *testing.T, c *gin.Context, target any, translator *i18n.Translator) error {
	t.Helper()
	err := c.ShouldBindJSON(target)
	if err == nil {
		return nil
	}
	if valErr, ok := errors.AsType[govalidator.ValidationErrors](err); ok {
		lang := c.GetHeader("Accept-Language")
		if translator != nil {
			lang = translator.Language(lang)
		}
		return errs.Validation(validator.Translate(translator, lang, valErr))
	}
	return err
}

// TestJSONRejectsEmptyBody 校验空请求体是 400 而非 422（api.md 4.2）。
func TestJSONRejectsEmptyBody(t *testing.T) {
	var payload signupPayload
	assertBadRequest(t, bindJSON(t, newContext(""), &payload, newTranslator(t, "en")))
}

// TestJSONRejectsMalformedJSON 校验非法 JSON 是 400。
func TestJSONRejectsMalformedJSON(t *testing.T) {
	var payload signupPayload
	assertBadRequest(t, bindJSON(t, newContext(`{"email":`), &payload, newTranslator(t, "en")))
}

// TestJSONRejectsWrongFieldType 校验字段类型不匹配是 400。
func TestJSONRejectsWrongFieldType(t *testing.T) {
	var payload signupPayload
	assertBadRequest(t, bindJSON(t, newContext(`{"email":42}`), &payload, newTranslator(t, "en")))
}

// TestJSONReportsCleanFieldPaths 校验错误键是纯字段路径，不含根结构体类型名。
func TestJSONReportsCleanFieldPaths(t *testing.T) {
	var payload signupPayload
	err := bindJSON(t, newContext(`{"email":"not-an-email","password":"short"}`), &payload, newTranslator(t, "en"))
	require.Error(t, err)

	status, resp := errs.Map(err)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "Validation failed", resp.Message)
	assert.Equal(t, []string{"The email address must be a valid email address."}, resp.Errors["email"])
	assert.Equal(t, []string{"The password must be at least 8 characters."}, resp.Errors["password"])
}

// TestCustomRules 校验自定义规则的接受与拒绝分支。
func TestCustomRules(t *testing.T) {
	translator := newTranslator(t, "en")

	valid := `{"email":"a@b.com","password":"secret123","username":"alice_01","phoneNumber":"+8613800138000"}`
	var accepted signupPayload
	require.NoError(t, bindJSON(t, newContext(valid), &accepted, translator))

	rejected := []struct {
		name string
		body string
		key  string
	}{
		{"用户名含大写", `{"email":"a@b.com","password":"secret123","username":"Alice"}`, "username"},
		{"用户名过短", `{"email":"a@b.com","password":"secret123","username":"ab"}`, "username"},
		{"用户名含连字符", `{"email":"a@b.com","password":"secret123","username":"a-b"}`, "username"},
		{"用户名保留字 admin", `{"email":"a@b.com","password":"secret123","username":"admin"}`, "username"},
		{"用户名保留字 api", `{"email":"a@b.com","password":"secret123","username":"api"}`, "username"},
		{"手机号缺国际区号", `{"email":"a@b.com","password":"secret123","phoneNumber":"13800138000"}`, "phoneNumber"},
		{"缺少必填字段", `{}`, "email"},
	}

	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			var payload signupPayload
			err := bindJSON(t, newContext(tt.body), &payload, translator)
			require.Error(t, err)

			status, resp := errs.Map(err)
			assert.Equal(t, http.StatusUnprocessableEntity, status)
			assert.Contains(t, resp.Errors, tt.key, "实际字段键: %v", resp.Errors)
		})
	}
}

// TestMinMaxWordingDependsOnKind 校验数值与字符串的 min 措辞分流。
func TestMinMaxWordingDependsOnKind(t *testing.T) {
	var probe numericProbe
	translator := newTranslator(t, "en")
	ctx := newContext(`{"count":0,"name":""}`)
	err := bindJSON(t, ctx, &probe, translator)
	require.Error(t, err)

	_, resp := errs.Map(err)
	assert.Equal(t, []string{"The count must be at least 1."}, resp.Errors["count"])
	assert.Equal(t, []string{"The name must be at least 1 characters."}, resp.Errors["name"])
}

// TestJSONOptionalToleratesEmptyBody 校验可省略请求体在空体时视为零值对象。
func TestJSONOptionalToleratesEmptyBody(t *testing.T) {
	var payload optionalBody
	require.NoError(t, validator.BindOptionalJSON(newContext(""), &payload))
	assert.Nil(t, payload.RefreshToken)
}

// TestJSONOptionalStillRejectsMalformedJSON 校验可省略请求体仍要求合法 JSON。
func TestJSONOptionalStillRejectsMalformedJSON(t *testing.T) {
	var payload optionalBody
	assertBadRequest(t, validator.BindOptionalJSON(newContext(`{"refresh`), &payload))
}

// TestLanguageNegotiatedFromHeader 校验校验文案随 Accept-Language 切换。
func TestLanguageNegotiatedFromHeader(t *testing.T) {
	translator := newTranslator(t, "en")
	ctx := newContext(`{"email":"bad","password":"short"}`)
	ctx.Request.Header.Set("Accept-Language", "zh-CN")

	assert.Equal(t, "zh-CN", translator.Language(ctx.GetHeader("Accept-Language")))

	var payload signupPayload
	err := bindJSON(t, ctx, &payload, translator)
	require.Error(t, err)

	_, resp := errs.Map(err)
	assert.Equal(t, []string{"邮箱地址必须是有效的邮箱地址。"}, resp.Errors["email"])
	assert.Equal(t, []string{"密码至少需要 8 个字符。"}, resp.Errors["password"])
}

// TestPasswordStrength 校验口令强度规则及其字段键与契约错误码。
func TestPasswordStrength(t *testing.T) {
	require.NoError(t, validator.PasswordStrength("password", "secret12345"))
	require.NoError(t, validator.PasswordStrength("password", "口令密码1234"))
	require.Error(t, validator.PasswordStrength("password", "onlyletters"))
	require.Error(t, validator.PasswordStrength("password", "12345678"))

	status, resp := errs.Map(validator.PasswordStrength("newPassword", "onlyletters"))
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, string(errs.CodeAuthPasswordTooWeak), resp.Code)
	assert.Contains(t, resp.Errors, "newPassword")
}

// TestPasswordValidationRule 验证基于 tag 的 password 校验规则与多语言翻译。
func TestPasswordValidationRule(t *testing.T) {
	require.NoError(t, validator.InitEngine())
	v, ok := binding.Validator.Engine().(*govalidator.Validate)
	require.True(t, ok)

	type testPasswordForm struct {
		Password string `json:"password" validate:"password"`
		Custom   string `json:"custom" validate:"omitempty,password=10"`
	}

	t.Run("有效密码验证通过", func(t *testing.T) {
		assert.NoError(t, v.Struct(testPasswordForm{Password: "secret123", Custom: "longPass123"}))
	})

	t.Run("纯字母未包含数字触发失败", func(t *testing.T) {
		err := v.Struct(testPasswordForm{Password: "onlyletters"})
		require.Error(t, err)
		valErr, ok := errors.AsType[govalidator.ValidationErrors](err)
		require.True(t, ok)
		assert.Equal(t, "password", valErr[0].Tag())
	})

	t.Run("纯数字未包含字母触发失败", func(t *testing.T) {
		err := v.Struct(testPasswordForm{Password: "12345678"})
		require.Error(t, err)
		valErr, ok := errors.AsType[govalidator.ValidationErrors](err)
		require.True(t, ok)
		assert.Equal(t, "password", valErr[0].Tag())
	})

	t.Run("长度不足 8 位触发失败", func(t *testing.T) {
		err := v.Struct(testPasswordForm{Password: "pass1"})
		require.Error(t, err)
		valErr, ok := errors.AsType[govalidator.ValidationErrors](err)
		require.True(t, ok)
		assert.Equal(t, "password", valErr[0].Tag())
	})

	t.Run("带参数长度不足触发失败", func(t *testing.T) {
		err := v.Struct(testPasswordForm{Password: "secret123", Custom: "pass12345"})
		require.Error(t, err)
		valErr, ok := errors.AsType[govalidator.ValidationErrors](err)
		require.True(t, ok)
		assert.Equal(t, "custom", valErr[0].Field())
		assert.Equal(t, "password", valErr[0].Tag())
	})

	t.Run("密码多语言报错翻译", func(t *testing.T) {
		trZH := newTranslator(t, "zh-CN")
		trEN := newTranslator(t, "en")

		err := v.Struct(testPasswordForm{Password: "12345678"})
		require.Error(t, err)
		valErr, ok := errors.AsType[govalidator.ValidationErrors](err)
		require.True(t, ok)

		transZH := validator.Translate(trZH, "zh-CN", valErr)
		assert.Equal(t, []string{"密码必须同时包含字母和数字。"}, transZH["password"])

		transEN := validator.Translate(trEN, "en", valErr)
		assert.Equal(t, []string{"The password must contain both letters and numbers."}, transEN["password"])
	})
}

// assertBadRequest 断言错误被映射为 400 BAD_REQUEST。
func assertBadRequest(t *testing.T, err error) {
	t.Helper()

	require.Error(t, err)
	status, resp := errs.Map(err)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, string(errs.CodeBadRequest), resp.Code)
}

// TestUsernameSlugAndE164LengthGuards 验证长度截断防护，防止超长字符串进入正则。
func TestUsernameSlugAndE164LengthGuards(t *testing.T) {
	translator := newTranslator(t, "en")

	// 1. 用户名超长 (65 字符)
	longUsername := "a" + string(bytes.Repeat([]byte("b"), 65))
	ctx1 := newContext(`{"email":"ok@example.com","password":"password1","username":"` + longUsername + `"}`)
	var payload1 signupPayload
	err1 := bindJSON(t, ctx1, &payload1, translator)
	require.Error(t, err1)
	_, resp1 := errs.Map(err1)
	assert.Contains(t, resp1.Errors, "username")

	// 2. 手机号超长 (> 20 字符)
	longPhone := "+86138" + string(bytes.Repeat([]byte("0"), 20))
	ctx2 := newContext(`{"email":"ok@example.com","password":"password1","phoneNumber":"` + longPhone + `"}`)
	var payload2 signupPayload
	err2 := bindJSON(t, ctx2, &payload2, translator)
	require.Error(t, err2)
	_, resp2 := errs.Map(err2)
	assert.Contains(t, resp2.Errors, "phoneNumber")

	// 3. 手机号过短 (< 8 字符)
	shortPhone := "+86"
	ctx3 := newContext(`{"email":"ok@example.com","password":"password1","phoneNumber":"` + shortPhone + `"}`)
	var payload3 signupPayload
	err3 := bindJSON(t, ctx3, &payload3, translator)
	require.Error(t, err3)
	_, resp3 := errs.Map(err3)
	assert.Contains(t, resp3.Errors, "phoneNumber")
}

// TestCustomTagConcurrentSafety 验证 sync.Map 高并发注册与查询自定义规则的线程安全。
func TestCustomTagConcurrentSafety(t *testing.T) {
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tag := fmt.Sprintf("custom_rule_%d", idx)
			_ = validator.RegisterRule(tag, func(fl govalidator.FieldLevel) bool {
				return true
			}, "custom_msg_key")
		}(i)
	}
	wg.Wait()
}

// createTestFileHeader 构造用于测试校验规则的 *multipart.FileHeader。
func createTestFileHeader(t *testing.T, fieldName, filename string, content []byte) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	require.NoError(t, req.ParseMultipartForm(10<<20))
	files := req.MultipartForm.File[fieldName]
	require.NotEmpty(t, files)
	return files[0]
}

// validPNGBytes 最小合法 PNG 图片二进制字节流（包含魔数头与基础块）。
var validPNGBytes = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

type testUploadForm struct {
	Avatar *multipart.FileHeader `form:"avatar" validate:"required,file,image,mimes=png jpg jpeg,maxsize=2M"`
}

// TestFileValidationRules 验证 file, image, maxsize, mimes 规则的判定与国际化翻译。
func TestFileValidationRules(t *testing.T) {
	require.NoError(t, validator.InitEngine())
	v, ok := binding.Validator.Engine().(*govalidator.Validate)
	require.True(t, ok)

	t.Run("合法图片验证通过", func(t *testing.T) {
		file := createTestFileHeader(t, "avatar", "test.png", validPNGBytes)
		form := testUploadForm{Avatar: file}
		err := v.Struct(form)
		assert.NoError(t, err)
	})

	t.Run("伪装图片 (文本伪装成 png) 触发 image 规则", func(t *testing.T) {
		file := createTestFileHeader(t, "avatar", "test.png", []byte("plain text content"))
		form := testUploadForm{Avatar: file}
		err := v.Struct(form)
		require.Error(t, err)

		valErr, ok := errors.AsType[govalidator.ValidationErrors](err)
		require.True(t, ok)
		assert.Equal(t, "image", valErr[0].Tag())
	})

	t.Run("尺寸超限触发 maxsize 规则", func(t *testing.T) {
		oversized := make([]byte, 2*1024*1024+10)
		copy(oversized, validPNGBytes)
		file := createTestFileHeader(t, "avatar", "large.png", oversized)
		form := testUploadForm{Avatar: file}
		err := v.Struct(form)
		require.Error(t, err)

		valErr, ok := errors.AsType[govalidator.ValidationErrors](err)
		require.True(t, ok)
		assert.Equal(t, "maxsize", valErr[0].Tag())
	})

	t.Run("扩展名不在白名单触发 mimes 规则", func(t *testing.T) {
		// gif 不在 mimes=png jpg jpeg 白名单中
		gifBytes := append([]byte("GIF89a"), validPNGBytes...)
		file := createTestFileHeader(t, "avatar", "animation.gif", gifBytes)
		form := testUploadForm{Avatar: file}
		err := v.Struct(form)
		require.Error(t, err)

		valErr, ok := errors.AsType[govalidator.ValidationErrors](err)
		require.True(t, ok)
		assert.Equal(t, "mimes", valErr[0].Tag())
	})

	t.Run("验证规则的多语言翻译 (en 与 zh-CN)", func(t *testing.T) {
		trEN := newTranslator(t, "en")
		trZH := newTranslator(t, "zh-CN")

		file := createTestFileHeader(t, "avatar", "test.txt", []byte("some text"))
		form := testUploadForm{Avatar: file}
		err := v.Struct(form)
		require.Error(t, err)

		valErr, ok := errors.AsType[govalidator.ValidationErrors](err)
		require.True(t, ok)

		transEN := validator.Translate(trEN, "en", valErr)
		require.Contains(t, transEN, "avatar")
		assert.Contains(t, transEN["avatar"][0], "must be an image")

		transZH := validator.Translate(trZH, "zh-CN", valErr)
		require.Contains(t, transZH, "avatar")
		assert.Contains(t, transZH["avatar"][0], "必须是图片")
	})
}

// complexRangeForm 模拟带跨字段校验的 DTO。
type complexRangeForm struct {
	StartTime int   `json:"startTime" validate:"required,min=1"`
	EndTime   int   `json:"endTime" validate:"required,min=1"`
	hookRun   *bool `json:"-"`
}

func (f *complexRangeForm) Validate() error {
	if f.hookRun != nil {
		*f.hookRun = true
	}
	if f.EndTime <= f.StartTime {
		return errs.Validation(map[string][]string{
			"endTime": {"结束时间必须晚于开始时间。"},
		})
	}
	return nil
}

type plainErrorForm struct {
	Value int `json:"value" validate:"min=1"`
}

func (f *plainErrorForm) Validate() error {
	if f.Value%2 != 0 {
		return errors.New("值必须为偶数")
	}
	return nil
}

// TestValidatableInterface 验证 DTO 自校验 Validatable 接口与短路逻辑。
func TestValidatableInterface(t *testing.T) {
	require.NoError(t, validator.InitEngine())

	t.Run("通过基础校验且通过 Validate 钩子", func(t *testing.T) {
		hookRun := false
		form := complexRangeForm{StartTime: 10, EndTime: 20, hookRun: &hookRun}
		err := binding.Validator.ValidateStruct(&form)
		require.NoError(t, err)
		assert.True(t, hookRun)
	})

	t.Run("基础标签校验未通过时 Fail-Fast 短路，不触发 Validate", func(t *testing.T) {
		hookRun := false
		// StartTime 为 0，不满足 min=1
		form := complexRangeForm{StartTime: 0, EndTime: 20, hookRun: &hookRun}
		err := binding.Validator.ValidateStruct(&form)
		require.Error(t, err)
		assert.False(t, hookRun, "Fail-Fast 保证在 tag 失败时不触发 Validate 钩子")
	})

	t.Run("基础标签通过但 Validate 返回 errs.Error 携带字段明细", func(t *testing.T) {
		hookRun := false
		// StartTime 10, EndTime 5，违反 EndTime > StartTime
		form := complexRangeForm{StartTime: 10, EndTime: 5, hookRun: &hookRun}
		err := binding.Validator.ValidateStruct(&form)
		require.Error(t, err)
		assert.True(t, hookRun)

		bizErr, ok := errs.As(err)
		require.True(t, ok)
		assert.Equal(t, string(errs.CodeValidationFailed), string(bizErr.Code()))
		assert.Equal(t, []string{"结束时间必须晚于开始时间。"}, bizErr.Fields()["endTime"])
	})

	t.Run("Validate 返回原生 error 时自动包装为 422 VALIDATION_FAILED", func(t *testing.T) {
		form := plainErrorForm{Value: 3} // 奇数
		err := binding.Validator.ValidateStruct(&form)
		require.Error(t, err)

		status, resp := errs.Map(err)
		assert.Equal(t, http.StatusUnprocessableEntity, status)
		assert.Equal(t, string(errs.CodeValidationFailed), resp.Code)
		assert.Equal(t, "值必须为偶数", resp.Message)
	})

	t.Run("通过 Gin ShouldBindJSON 完整链路触发 Validatable", func(t *testing.T) {
		translator := newTranslator(t, "zh-CN")
		ctx := newContext(`{"startTime":100,"endTime":50}`)
		var form complexRangeForm
		err := bindJSON(t, ctx, &form, translator)
		require.Error(t, err)

		status, resp := errs.Map(err)
		assert.Equal(t, http.StatusUnprocessableEntity, status)
		assert.Equal(t, string(errs.CodeValidationFailed), resp.Code)
		assert.Contains(t, resp.Errors, "endTime")
	})
}

type testRoleType string

const (
	roleSuper testRoleType = "super"
	roleGuest testRoleType = "guest"
)

type testPatchBody struct {
	Name omitnull.Val[string]       `json:"name" validate:"omitempty,min=3,max=20"`
	Age  omitnull.Val[int]          `json:"age" validate:"omitempty,min=1,max=120"`
	Role omitnull.Val[testRoleType] `json:"role" validate:"omitempty,oneof=super guest"`
}

// TestOmitNullValidation 验证三态 omitnull 容器字段与验证器解包、标量规则联动的准确性。
func TestOmitNullValidation(t *testing.T) {
	require.NoError(t, validator.InitEngine())
	validator.RegisterOmitNullTypes(omitnull.Val[testRoleType]{})

	v, ok := binding.Validator.Engine().(*govalidator.Validate)
	require.True(t, ok)

	t.Run("全部字段未设置 (Unset) 时通过", func(t *testing.T) {
		form := testPatchBody{}
		assert.NoError(t, v.Struct(form))
	})

	t.Run("显式置空 (Null) 时通过 omitempty 规则", func(t *testing.T) {
		form := testPatchBody{
			Name: omitnull.FromPtr[string](nil),
			Age:  omitnull.FromPtr[int](nil),
		}
		assert.NoError(t, v.Struct(form))
	})

	t.Run("包含合法有效值时校验通过", func(t *testing.T) {
		form := testPatchBody{
			Name: omitnull.From("alice"),
			Age:  omitnull.From(25),
			Role: omitnull.From(roleSuper),
		}
		assert.NoError(t, v.Struct(form))
	})

	t.Run("包含非法值时触发标量标签规则拦截", func(t *testing.T) {
		form := testPatchBody{
			Name: omitnull.From("al"), // < min=3
			Age:  omitnull.From(200),  // > max=120
			Role: omitnull.From(testRoleType("invalid_role")),
		}
		err := v.Struct(form)
		require.Error(t, err)

		valErr, ok := errors.AsType[govalidator.ValidationErrors](err)
		require.True(t, ok)
		assert.Len(t, valErr, 3)

		tags := map[string]string{}
		for _, fe := range valErr {
			tags[fe.Field()] = fe.Tag()
		}
		assert.Equal(t, "min", tags["name"])
		assert.Equal(t, "max", tags["age"])
		assert.Equal(t, "oneof", tags["role"])
	})
}
