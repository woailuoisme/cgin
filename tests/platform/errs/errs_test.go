// Package errs_test 以黑盒方式验证错误映射契约（api.md 5.2 与 4.2）。
package errs_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/samber/oops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"bgin/internal/platform/errs"
)

// api.md 5.2 明文规定的两条文案，不得随意改动。
const (
	msgValidationFailed = "Validation failed"
	msgInternal         = "An unexpected error occurred"
)

// payload 覆盖嵌套结构与切片字段，用于验证字段路径的规范化。
type payload struct {
	Email string `json:"email" validate:"required,email"`
	Tags  []tag  `json:"tags" validate:"dive"`
}

type tag struct {
	Name string `json:"name" validate:"required"`
}

// TestMapBusinessError 校验业务错误按登记的状态码与错误码输出。
func TestMapBusinessError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   errs.Code
	}{
		{"登录失败", errs.InvalidCredentials(), http.StatusUnauthorized, errs.CodeAuthInvalidCredentials},
		{"账号封禁", errs.New(errs.CodeAuthAccountBanned, "banned"), http.StatusForbidden, errs.CodeAuthAccountBanned},
		{"邮箱重复", errs.New(errs.CodeAuthEmailAlreadyExists, "dup"), http.StatusConflict, errs.CodeAuthEmailAlreadyExists},
		{"用户名占用", errs.New(errs.CodeAuthUsernameTaken, "taken"), http.StatusConflict, errs.CodeAuthUsernameTaken},
		{"令牌过期", errs.Unauthorized(errs.CodeAuthTokenExpired, "expired"), http.StatusUnauthorized, errs.CodeAuthTokenExpired},
		{"令牌已撤销", errs.Unauthorized(errs.CodeAuthTokenRevoked, "revoked"), http.StatusUnauthorized, errs.CodeAuthTokenRevoked},
		{"验证码错误", errs.New(errs.CodeAuthOTPInvalid, "bad code"), http.StatusUnprocessableEntity, errs.CodeAuthOTPInvalid},
		{"验证码过期", errs.New(errs.CodeAuthOTPExpired, "expired code"), http.StatusUnprocessableEntity, errs.CodeAuthOTPExpired},
		{"口令不匹配", errs.New(errs.CodeAuthPasswordMismatch, "mismatch"), http.StatusUnprocessableEntity, errs.CodeAuthPasswordMismatch},
		{"请求体格式", errs.BadRequest("bad"), http.StatusBadRequest, errs.CodeBadRequest},
		{"资源不存在", errs.NotFound("missing"), http.StatusNotFound, errs.CodeNotFound},
		{"无权访问", errs.Forbidden("nope"), http.StatusForbidden, errs.CodeForbidden},
		{"状态冲突", errs.Conflict("conflict"), http.StatusConflict, errs.CodeConflict},
		{"限流", errs.RateLimited("slow down"), http.StatusTooManyRequests, errs.CodeRateLimited},
		{"客户端主动断开", errs.ClientClosedRequest(context.Canceled), errs.StatusClientClosedRequest, errs.CodeClientClosedRequest},
		{"内部错误", errs.Internal(errors.New("boom")), http.StatusInternalServerError, errs.CodeInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, resp := errs.Map(tt.err)
			assert.Equal(t, tt.wantStatus, status)
			assert.Equal(t, string(tt.wantCode), resp.Code)
			assert.NotEmpty(t, resp.Message)
		})
	}
}

// TestMapValidationErrorNormalisesFieldPaths 校验未包装的 validator 错误也能给出
// 干净的字段路径：无根类型名、索引写成点号（api.md 5.2 的 users.0.email 形态）。
func TestMapValidationErrorNormalisesFieldPaths(t *testing.T) {
	instance := validator.New()
	instance.RegisterTagNameFunc(func(field reflect.StructField) string {
		if name := field.Tag.Get("json"); name != "" {
			return name
		}
		return field.Name
	})

	status, resp := errs.Map(instance.Struct(payload{Tags: []tag{{}}}))

	require.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, string(errs.CodeValidationFailed), resp.Code)
	assert.Equal(t, msgValidationFailed, resp.Message)
	assert.Contains(t, resp.Errors, "email")
	assert.Contains(t, resp.Errors, "tags.0.name", "实际字段键: %v", resp.Errors)
}

// TestMapPgUniqueViolation 校验唯一约束冲突被翻译成 409 且不泄漏数据库细节。
func TestMapPgUniqueViolation(t *testing.T) {
	status, resp := errs.Map(&pgconn.PgError{
		Code:           "23505",
		ConstraintName: "idx_user_email",
		Detail:         "Key (lower(email))=(a@b.c) already exists.",
	})

	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, string(errs.CodeConflict), resp.Code)
	assert.NotContains(t, resp.Message, "already exists", "不应把数据库细节透给调用方")
}

// TestMapPgForeignKeyAndNotNull 校验外键与非空约束归为 409。
func TestMapPgForeignKeyAndNotNull(t *testing.T) {
	for _, code := range []string{"23503", "23502"} {
		status, _ := errs.Map(&pgconn.PgError{Code: code})
		assert.Equal(t, http.StatusConflict, status, "PgError %s", code)
	}
}

// TestMapNoRows 校验 sql.ErrNoRows 与 pgx.ErrNoRows 都归为 404。
func TestMapNoRows(t *testing.T) {
	for _, err := range []error{sql.ErrNoRows, pgx.ErrNoRows} {
		status, resp := errs.Map(err)
		assert.Equal(t, http.StatusNotFound, status)
		assert.Equal(t, string(errs.CodeNotFound), resp.Code)
	}
}

// TestMapContextCanceled 校验 context.Canceled 与客户端主动取消映射为 499 CLIENT_CLOSED_REQUEST。
func TestMapContextCanceled(t *testing.T) {
	for _, err := range []error{
		context.Canceled,
		errs.ClientClosedRequest(context.Canceled),
		oops.Code(string(errs.CodeContextCanceled)).Wrap(context.Canceled),
	} {
		status, resp := errs.Map(err)
		assert.Equal(t, errs.StatusClientClosedRequest, status)
		assert.Equal(t, string(errs.CodeClientClosedRequest), resp.Code)
		assert.Equal(t, "Client closed request", resp.Message)
	}
}

// TestMapUnexpectedError 校验未识别错误一律收敛为 500，且不泄漏原始信息。
func TestMapUnexpectedError(t *testing.T) {
	status, resp := errs.Map(errors.New("some internal detail leaked via driver"))

	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, string(errs.CodeInternal), resp.Code)
	assert.Equal(t, msgInternal, resp.Message)
	assert.NotContains(t, resp.Message, "driver")
}

// TestMapOOpsOnlyError 校验平台层直接抛出的、已登记错误码的 oops 错误。
func TestMapOOpsOnlyError(t *testing.T) {
	err := oops.Code(string(errs.CodeAuthOTPInvalid)).Public("The verification code is incorrect").New("otp")

	status, resp := errs.Map(err)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, string(errs.CodeAuthOTPInvalid), resp.Code)
	assert.Equal(t, "The verification code is incorrect", resp.Message)
}

// TestMapUnregisteredOOpsCodeFallsBackTo500 校验未登记的错误码不会漏出成 2xx。
func TestMapUnregisteredOOpsCodeFallsBackTo500(t *testing.T) {
	status, resp := errs.Map(oops.Code("TOTALLY_UNKNOWN").New("x"))

	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, string(errs.CodeInternal), resp.Code)
}

// TestValidationCarriesFieldDetails 校验 422 响应体携带按字段路径归集的明细。
func TestValidationCarriesFieldDetails(t *testing.T) {
	status, resp := errs.Map(errs.Validation(map[string][]string{
		"authorization.role": {"The selected authorization.role is invalid."},
		"users.0.email":      {"The users.0.email field is required."},
	}))

	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Len(t, resp.Errors, 2)
	assert.Len(t, resp.Errors["users.0.email"], 1)
}

// TestWrapKeepsCause 校验错误包裹后仍可用 errors.Is 命中原始错误。
func TestWrapKeepsCause(t *testing.T) {
	err := errs.Wrap(errs.CodeNotFound, "missing", sql.ErrNoRows)
	require.ErrorIs(t, err, sql.ErrNoRows)

	extracted, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeNotFound, extracted.Code())
	assert.Equal(t, "missing", extracted.Message())
}

// TestWrapNilCauseDegradesToNew 校验空 cause 不会 panic 也不会丢失错误码。
func TestWrapNilCauseDegradesToNew(t *testing.T) {
	err := errs.Wrap(errs.CodeNotFound, "missing", nil)

	extracted, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeNotFound, extracted.Code())
}

// TestWithFieldAccumulates 校验同一字段的多条明细会累积而非覆盖。
func TestWithFieldAccumulates(t *testing.T) {
	err := errs.New(errs.CodeValidationFailed, msgValidationFailed).
		WithField("name", "first").
		WithField("name", "second")

	assert.Equal(t, []string{"first", "second"}, err.Fields()["name"])
}

// TestNormalizePath 校验索引路径改写为点号分隔。
func TestNormalizePath(t *testing.T) {
	assert.Equal(t, "users.0.email", errs.NormalizePath("users[0].email"))
	assert.Equal(t, "a.0.1.b", errs.NormalizePath("a[0][1].b"))
	assert.Equal(t, "plain", errs.NormalizePath("plain"))
}

// TestStatusCoversEveryDeclaredCode 校验每个公开错误码都有明确的状态码，
// 不会因为漏登记而悄悄退化成 500。
func TestStatusCoversEveryDeclaredCode(t *testing.T) {
	tests := []struct {
		code       errs.Code
		wantStatus int
	}{
		{errs.CodeBadRequest, http.StatusBadRequest},
		{errs.CodeValidationFailed, http.StatusUnprocessableEntity},
		{errs.CodeNotFound, http.StatusNotFound},
		{errs.CodeMethodNotAllowed, http.StatusMethodNotAllowed},
		{errs.CodeForbidden, http.StatusForbidden},
		{errs.CodeConflict, http.StatusConflict},
		{errs.CodeRateLimited, http.StatusTooManyRequests},
		{errs.CodeRequestTooLarge, http.StatusRequestEntityTooLarge},
		{errs.CodeInternal, http.StatusInternalServerError},
		{errs.CodeAuthInvalidCredentials, http.StatusUnauthorized},
		{errs.CodeAuthEmailAlreadyExists, http.StatusConflict},
		{errs.CodeAuthUsernameTaken, http.StatusConflict},
		{errs.CodeAuthAccountBanned, http.StatusForbidden},
		{errs.CodeAuthEmailNotVerified, http.StatusForbidden},
		{errs.CodeAuthOTPInvalid, http.StatusUnprocessableEntity},
		{errs.CodeAuthOTPExpired, http.StatusUnprocessableEntity},
		{errs.CodeAuthOTPTooManyAttempts, http.StatusTooManyRequests},
		{errs.CodeAuthOTPRateLimited, http.StatusTooManyRequests},
		{errs.CodeAuthTokenInvalid, http.StatusUnauthorized},
		{errs.CodeAuthTokenExpired, http.StatusUnauthorized},
		{errs.CodeAuthTokenRevoked, http.StatusUnauthorized},
		{errs.CodeAuthPasswordMismatch, http.StatusUnprocessableEntity},
		{errs.CodeAuthPasswordTooWeak, http.StatusUnprocessableEntity},
		{errs.CodeGatewayTimeout, http.StatusGatewayTimeout},

		// 子系统错误码：这些是"客户端改一下请求就能成功"的失败，
		// 漏登记会让它们静默退化成 500，与 api.md 的状态码契约不符。
		{errs.CodeExcelOpenError, http.StatusBadRequest},
		{errs.CodeExcelReadError, http.StatusBadRequest},
		{errs.CodeExcelFileTooLarge, http.StatusRequestEntityTooLarge},
		{errs.CodeExcelMapError, http.StatusUnprocessableEntity},
		{errs.CodeExcelRowsError, http.StatusUnprocessableEntity},
		{errs.CodeExcelReadRowError, http.StatusUnprocessableEntity},
		{errs.CodeEmptySheets, http.StatusBadRequest},
		{errs.CodeCSVReadError, http.StatusBadRequest},
		{errs.CodeNotificationMissingRecipient, http.StatusBadRequest},
		{errs.CodeNotificationMissingRecipientID, http.StatusBadRequest},
		{errs.CodeNotificationUnsupportedSMS, http.StatusBadRequest},
		{errs.CodeNotificationUnsupportedMail, http.StatusBadRequest},
		{errs.CodeNotificationUnsupportedDB, http.StatusBadRequest},
		{errs.CodeNotificationUnsupportedRT, http.StatusBadRequest},
		{errs.CodeNotificationUnsupportedGotify, http.StatusBadRequest},
		{errs.CodeNotificationUnsupportedChat, http.StatusBadRequest},
		{errs.CodeNotificationChannelNotFound, http.StatusBadRequest},
		{errs.CodeNotificationRateLimited, http.StatusTooManyRequests},
		{errs.CodeNotificationDeduplicated, http.StatusConflict},
		{errs.CodeMailMissingRecipient, http.StatusBadRequest},
		{errs.CodeSMSMissingPhone, http.StatusBadRequest},
		{errs.CodeInvalidSMSMessage, http.StatusBadRequest},
		{errs.CodeInvalidGotifyMessage, http.StatusBadRequest},
		{errs.CodeInvalidChatMessage, http.StatusBadRequest},
		{errs.CodePaymentCapabilityNotSupported, http.StatusBadRequest},
		{errs.CodeDatabaseUnavailable, http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.wantStatus, errs.Status(tt.code), "错误码 %s 的状态码映射不对", tt.code)
	}

	// 未登记的错误码必须兜底为 500，而不是漏成 2xx。
	assert.Equal(t, http.StatusInternalServerError, errs.Status(errs.Code("NOT_REGISTERED")))
}

// codesDeclPattern / apiDocsCodePattern 用于把两份"错误码清单"抽出来做双向比对。
var (
	codesDeclPattern = regexp.MustCompile(`Code[A-Za-z0-9]+\s+Code = "([^"]+)"`)
	// apiDocsCodePattern 匹配 api.md 错误码表格行的首列（形如 | `BAD_REQUEST` |）。
	// 只认全大写 token：这样表头行（| `code` | 状态码 | ...）不会被误当成一个码，
	// 同时把"文档里出现小写码"这种情况直接排除在合法清单之外。
	apiDocsCodePattern = regexp.MustCompile("(?m)^\\|\\s*`([A-Z][A-Z0-9_]*)`")
	shoutySnakeCase    = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// repoFile 读取仓库内文件的内容。
// 相对路径会随 CWD 变化（IDE 直跑测试、`go test -C` 都会换目录），故以本文件位置反推仓库根。
func repoFile(t *testing.T, rel string) []byte {
	t.Helper()

	for _, root := range repoRootCandidates() {
		if data, err := os.ReadFile(filepath.Join(root, rel)); err == nil {
			return data
		}
	}

	t.Fatalf("无法定位仓库文件：%s", rel)
	return nil
}

// repoRootCandidates 给出候选仓库根：本文件位置上溯三级，以及从 CWD 上溯找到 go.mod 的目录。
// 后者用于 `-trimpath` 构建——那时 runtime.Caller 只返回模块相对路径，无法直接拼接。
func repoRootCandidates() []string {
	candidates := make([]string, 0, 2)

	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(file), "..", "..", ".."))
	}

	dir, err := os.Getwd()
	for err == nil {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return append(candidates, dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return candidates
}

// TestCodeContractMatchesAPIDocs 锁定 codes.go 与 api.md 4.2 清单的双向一致。
//
// 这两处一旦漂移，客户端按文档分支就会匹配不上真实响应（历史上正是这么错的：
// codes.go 写小写、api.md 写大写，同一个码两种拼法）。
func TestCodeContractMatchesAPIDocs(t *testing.T) {
	source := repoFile(t, "internal/platform/errs/codes.go")

	declared := make([]string, 0, 64)
	for _, m := range codesDeclPattern.FindAllStringSubmatch(string(source), -1) {
		declared = append(declared, m[1])
	}
	require.NotEmpty(t, declared, "未能从 codes.go 解析出任何错误码声明")

	// 1. 声明值必须一律 SHOUTY_SNAKE_CASE：对外契约不接受小写码。
	for _, code := range declared {
		assert.Regexp(t, shoutySnakeCase, code, "错误码 %q 不是全大写下划线格式", code)
	}

	// 2. 会出现在响应体里的码（已在 statuses 登记）必须逐条写在 api.md 清单里。
	exposed := make([]string, 0, len(declared))
	for _, c := range errs.ExposedCodes() {
		exposed = append(exposed, string(c))
	}
	slices.Sort(exposed)

	docs := repoFile(t, "api.md")

	documented := make([]string, 0, len(exposed))
	for _, m := range apiDocsCodePattern.FindAllStringSubmatch(string(docs), -1) {
		documented = append(documented, m[1])
	}
	slices.Sort(documented)

	assert.Equal(t, exposed, documented,
		"api.md 4.2 清单与 errs.ExposedCodes() 必须一一对应：文档多写或漏写都会让客户端分支失效")

	// 3. api.md 里出现的每个码都必须在 codes.go 有声明（杜绝文档凭空造码）。
	declaredSet := make(map[string]struct{}, len(declared))
	for _, code := range declared {
		declaredSet[code] = struct{}{}
	}
	for _, code := range documented {
		_, ok := declaredSet[code]
		assert.True(t, ok, "api.md 列出的 %q 在 codes.go 中没有声明", code)
	}
}

// TestMapSubsystemCodeMessage 锁定子系统错误码的对外文案与状态码。
//
// 这类错误由平台子系统直接以 oops.Code("字面量") 抛出，不带 Public 文案；
// 登记进 statuses 后必须按状态码给出对应说明，而不是统一回一句"意外错误"。
func TestMapSubsystemCodeMessage(t *testing.T) {
	status, resp := errs.Map(oops.Code(errs.CodeExcelOpenError).Wrap(errors.New("zip: not a valid zip file")))
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, string(errs.CodeExcelOpenError), resp.Code)
	assert.Equal(t, "EXCEL_OPEN_ERROR", resp.Code)
	assert.NotEqual(t, msgInternal, resp.Message)

	// 未登记的码保持原有兜底行为：500 + 通用文案，且不回显底层原因。
	status, resp = errs.Map(oops.Code("some_internal_failure").Wrap(errors.New("driver exploded")))
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, string(errs.CodeInternal), resp.Code)
	assert.Equal(t, msgInternal, resp.Message)
	assert.NotContains(t, resp.Message, "driver exploded")
}

// TestGatewayTimeoutMapping 校验 context.DeadlineExceeded 与 GatewayTimeout 映射为 504。
func TestGatewayTimeoutMapping(t *testing.T) {
	status, resp := errs.Map(context.DeadlineExceeded)
	assert.Equal(t, http.StatusGatewayTimeout, status)
	assert.Equal(t, string(errs.CodeGatewayTimeout), resp.Code)

	status, resp = errs.Map(errs.GatewayTimeout(context.DeadlineExceeded))
	assert.Equal(t, http.StatusGatewayTimeout, status)
	assert.Equal(t, string(errs.CodeGatewayTimeout), resp.Code)
}

// TestStackIsAvailableForLogging 校验 oops 堆栈可被提取，便于 500 日志定位。
func TestStackIsAvailableForLogging(t *testing.T) {
	err := errs.Internal(errors.New("boom"))
	assert.NotEmpty(t, errs.Stack(err))
}

// TestSanitizeMessage 校验错误文案脱敏机制，确保 SQL、密码等敏感特征被置换为安全文案。
func TestSanitizeMessage(t *testing.T) {
	assert.Equal(t, "Safe user message", errs.SanitizeMessage("Safe user message"))
	assert.Equal(t, "An unexpected error occurred", errs.SanitizeMessage("pq: relation users not found"))
	assert.Equal(t, "An unexpected error occurred", errs.SanitizeMessage("failed with password=123456"))
	assert.Equal(t, "An unexpected error occurred", errs.SanitizeMessage("syntax error at SELECT * FROM secret"))
}

// TestRender_Sanitization 验证 Render 执行时敏感信息被安全清洗。
func TestRender_Sanitization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)

	// 触发带敏感信息的消息
	err := errs.BadRequest("failed: select id from users where token=xyz")
	errs.Render(c, err)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp errs.Response
	unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, unmarshalErr)
	assert.Equal(t, "An unexpected error occurred", resp.Message)

	assert.Equal(t, string(errs.CodeBadRequest), resp.Code)
}

// TestOopsFields 验证 oops 绑定的上下文能被自动解包为 Zap 字段，且敏感键值被安全脱敏。
func TestOopsFields(t *testing.T) {
	extractValue := func(f zap.Field) any {
		if f.String != "" {
			return f.String
		}
		if f.Integer != 0 {
			return int(f.Integer)
		}
		return f.Interface
	}

	t.Run("非 oops 错误返回 nil", func(t *testing.T) {
		assert.Nil(t, errs.OopsFields(errors.New("standard error")))
	})

	t.Run("普通字段正常提取且按 key 排序", func(t *testing.T) {
		err := oops.
			With("trade_no", "TX123456").
			With("channel", "wechat").
			Wrap(errors.New("db error"))

		fields := errs.OopsFields(err)
		require.Len(t, fields, 2)
		assert.Equal(t, "channel", fields[0].Key)
		assert.Equal(t, "wechat", extractValue(fields[0]))
		assert.Equal(t, "trade_no", fields[1].Key)
		assert.Equal(t, "TX123456", extractValue(fields[1]))
	})

	t.Run("敏感键名自动脱敏为星号", func(t *testing.T) {
		err := oops.
			With("password", "super-secret-123").
			With("api_key", "sk-live-xyz").
			With("access_token", "eyJhbGciOi...").
			With("normal_id", 1001).
			Wrap(errors.New("auth error"))

		fields := errs.OopsFields(err)
		require.Len(t, fields, 4)

		fieldMap := make(map[string]any)
		for _, f := range fields {
			fieldMap[f.Key] = extractValue(f)
		}

		assert.Equal(t, "***", fieldMap["password"])
		assert.Equal(t, "***", fieldMap["api_key"])
		assert.Equal(t, "***", fieldMap["access_token"])
		assert.Equal(t, 1001, fieldMap["normal_id"])
	})

	t.Run("敏感值包含 SQL 特征自动清洗", func(t *testing.T) {
		err := oops.
			With("query_preview", "select * from users where secret=123").
			Wrap(errors.New("sql failure"))

		fields := errs.OopsFields(err)
		require.Len(t, fields, 1)
		assert.Equal(t, "query_preview", fields[0].Key)
		assert.Equal(t, msgInternal, extractValue(fields[0]))
	})
}
