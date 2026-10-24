package errs

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/samber/oops"
	"go.uber.org/zap"

	"bgin/internal/platform/i18n"
	"bgin/internal/platform/logger"
)

// PostgreSQL 错误码，用于把约束冲突翻译成对调用方有意义的响应。
const (
	pgErrUniqueViolation           = "23505"
	pgErrForeignKeyViolation       = "23503"
	pgErrNotNullViolation          = "23502"
	pgErrInvalidTextRepresentation = "22P02"
)

// Response 是统一错误响应体。
type Response struct {
	Code    string              `json:"code"`
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors,omitempty"`
}

// 敏感词特征，用于安全脱敏防止底层 SQL、连接信息与凭证外泄。
var sensitivePatterns = []string{
	"password=", "secret=", "token=", "authorization:",
	"select ", "insert into", "update ", "delete from", "drop table",
	"pq: ", "pgx: ", "sql: ",
}

// SanitizeMessage 对对外输出的错误描述进行脱敏与安全清洗。
func SanitizeMessage(msg string) string {
	lower := strings.ToLower(msg)
	if slices.ContainsFunc(sensitivePatterns, func(p string) bool {
		return strings.Contains(lower, p)
	}) {
		return msgInternal
	}
	return msg
}

// errorCodeContextKey 是 gin 上下文里承载已渲染错误码的键。
//
// 键名只在本包出现，读写一律走下面两个 Helper：AGENTS §3.6 禁止跨包裸字符串 Context 键，
// 且这一对读写分散在 errs 与 middleware 两个包，裸字符串极易改一边漏一边。
const errorCodeContextKey = "error_code"

// SetErrorCodeContext 把最终错误码写入 gin 上下文，供访问日志与监控采集。
func SetErrorCodeContext(c *gin.Context, code string) {
	if c == nil || code == "" {
		return
	}
	c.Set(errorCodeContextKey, code)
}

// ErrorCodeFromContext 读取 gin 上下文中的错误码，缺失或类型不符时返回空串。
func ErrorCodeFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	code, ok := c.Get(errorCodeContextKey)
	if !ok {
		return ""
	}
	str, _ := code.(string)
	return str
}

// Render 映射统一错误响应并写回客户端。
//
// 5xx 记录错误根因与调用堆栈；4xx 由外层 AccessLog 统一输出访问日志，避免重复告警。
// 可选接收 *i18n.Translator 实例以优先进行业务字段级错误翻译，未传入时兜底使用 i18n.Default()。
func Render(c *gin.Context, err error, customTranslator ...*i18n.Translator) {
	if err == nil {
		return
	}

	status, resp := Map(err)

	// 敏感错误信息脱敏保护
	resp.Message = SanitizeMessage(resp.Message)

	// 针对携带待翻译 fieldKeys 的业务错误，自动尝试使用注入的 i18n 或全局默认与请求头 Accept-Language 完成翻译
	if len(resp.Errors) == 0 {
		if translated := resolveTranslatedFieldKeys(c, err, customTranslator...); translated != nil {
			resp.Errors = translated
		}
	}

	SetErrorCodeContext(c, resp.Code)
	if len(c.Errors) == 0 {
		_ = c.Error(err)
	}

	if status >= http.StatusInternalServerError {
		// 5xx 记录底层根因与调用堆栈，避免对外统一文案遮蔽排错细节。
		log := logger.FromContext(c.Request.Context())
		zapFields := []zap.Field{
			zap.String("stack", Stack(err)),
			zap.Error(RootCause(err)),
			zap.Int("status", status),
			zap.String("code", resp.Code),
		}
		if extra := OopsFields(err); len(extra) > 0 {
			zapFields = append(zapFields, extra...)
		}
		log.Error("request processing failed", zapFields...)
	}

	c.AbortWithStatusJSON(status, resp)
}

var sensitiveKeyFragments = []string{
	"password", "secret", "token", "credential", "auth", "key",
}

func isSensitiveKey(k string) bool {
	lower := strings.ToLower(k)
	return slices.ContainsFunc(sensitiveKeyFragments, func(frag string) bool {
		return strings.Contains(lower, frag)
	})
}

func sanitizeValue(v any) any {
	if s, ok := v.(string); ok {
		return SanitizeMessage(s)
	}
	return v
}

// OopsFields 提取错误链中 oops 绑定的上下文信息，并自动对敏感键与敏感值执行脱敏处理。
func OopsFields(err error) []zap.Field {
	oopsErr, ok := oops.AsOops(err)
	if !ok {
		return nil
	}

	ctxMap := oopsErr.Context()
	if len(ctxMap) == 0 {
		return nil
	}

	fields := make([]zap.Field, 0, len(ctxMap))
	for k, v := range ctxMap {
		if isSensitiveKey(k) {
			fields = append(fields, zap.String(k, "***"))
			continue
		}
		fields = append(fields, zap.Any(k, sanitizeValue(v)))
	}

	slices.SortFunc(fields, func(a, b zap.Field) int {
		return cmp.Compare(a.Key, b.Key)
	})

	return fields
}

// RootCause 提取用于日志记录的底层真实错误。
func RootCause(err error) error {
	if bizErr, ok := As(err); ok {
		if inner := errors.Unwrap(bizErr); inner != nil {
			return inner
		}
	}
	return err
}

// Map 把任意 error 归一化为 HTTP 状态码与响应体。映射顺序即优先级。
func Map(err error) (int, Response) {
	if bizErr, ok := As(err); ok {
		return Status(bizErr.code), Response{
			Code:    string(bizErr.code),
			Message: bizErr.message,
			Errors:  bizErr.fields,
		}
	}

	// 请求体结构错误（JSON 语法、类型不匹配、空体）统一按 400 处理。
	if status, resp, ok := mapMalformedBody(err); ok {
		return status, resp
	}

	if valErr, ok := errors.AsType[validator.ValidationErrors](err); ok {
		return http.StatusUnprocessableEntity, Response{
			Code:    string(CodeValidationFailed),
			Message: msgValidationFailed,
			Errors:  rawValidationFields(valErr),
		}
	}

	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return mapPgError(pgErr)
	}

	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return http.StatusNotFound, Response{Code: string(CodeNotFound), Message: msgNotFound}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, Response{Code: string(CodeGatewayTimeout), Message: msgGatewayTimeout}
	}

	if errors.Is(err, context.Canceled) {
		return StatusClientClosedRequest, Response{Code: string(CodeClientClosedRequest), Message: msgClientClosedRequest}
	}

	// 识别已在 statuses 登记过的 oops 错误。
	// 未主动声明对外文案时按状态码兜底，避免 4xx 也回一句"意外错误"。
	if oopsErr, ok := oops.AsOops(err); ok {
		code := Code(fmt.Sprintf("%v", oopsErr.Code()))
		if code == CodeContextCanceled {
			code = CodeClientClosedRequest
		}
		if status, registered := statuses[code]; registered {
			return status, Response{
				Code:    string(code),
				Message: cmp.Or(oopsErr.Public(), MessageForStatus(status)),
			}
		}
	}

	// 兜底：未识别错误统一返回 500。
	return http.StatusInternalServerError, Response{Code: string(CodeInternal), Message: msgInternal}
}

// mapMalformedBody 识别请求体解析、JSON 解码、查询参数转换与载荷超限的失败。
func mapMalformedBody(err error) (int, Response, bool) {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return http.StatusRequestEntityTooLarge, Response{
			Code:    string(CodeRequestTooLarge),
			Message: msgRequestTooLarge,
		}, true
	}
	if isBadRequestError(err) {
		return http.StatusBadRequest, Response{
			Code:    string(CodeBadRequest),
			Message: msgBadRequest,
		}, true
	}

	return 0, Response{}, false
}

func isBadRequestError(err error) bool {
	_, isNum := errors.AsType[*strconv.NumError](err)
	_, isSyntax := errors.AsType[*json.SyntaxError](err)
	_, isType := errors.AsType[*json.UnmarshalTypeError](err)
	return isNum || isSyntax || isType || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// mapPgError 将数据库约束错误映射为业务状态码与文案。
func mapPgError(err *pgconn.PgError) (int, Response) {
	switch err.Code {
	case pgErrUniqueViolation, pgErrForeignKeyViolation, pgErrNotNullViolation:
		return http.StatusConflict, Response{Code: string(CodeConflict), Message: msgConflict}
	case pgErrInvalidTextRepresentation:
		return http.StatusBadRequest, Response{Code: string(CodeBadRequest), Message: msgBadRequest}
	default:
		return http.StatusInternalServerError, Response{Code: string(CodeInternal), Message: msgInternal}
	}
}

// resolveTranslatedFieldKeys 针对携带待翻译 fieldKeys 的业务错误，自动使用注入的 translator 或全局 i18n 完成翻译。
func resolveTranslatedFieldKeys(c *gin.Context, err error, customTranslator ...*i18n.Translator) map[string][]string {
	bizErr, ok := As(err)
	if !ok || len(bizErr.fieldKeys) == 0 {
		return nil
	}

	var translator *i18n.Translator
	if len(customTranslator) > 0 && customTranslator[0] != nil {
		translator = customTranslator[0]
	} else {
		translator = i18n.Default()
	}
	if translator == nil {
		return nil
	}

	lang := translator.Language(c.GetHeader("Accept-Language"))
	translated := make(map[string][]string, len(bizErr.fieldKeys))
	for field, msgKeys := range bizErr.fieldKeys {
		for _, mk := range msgKeys {
			rendered := translator.ValidationMessage(lang, mk.Key, field, mk.Param)
			if rendered != "" {
				translated[field] = append(translated[field], rendered)
			}
		}
	}

	if len(translated) == 0 {
		return nil
	}
	return translated
}
