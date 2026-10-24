package errs

import (
	"cmp"
	"errors"

	"github.com/samber/oops"
)

// 面向调用方的固定描述文案。
const (
	msgValidationFailed    = "Validation failed"
	msgInternal            = "An unexpected error occurred"
	msgBadRequest          = "The request could not be understood"
	msgNotFound            = "The requested resource was not found"
	msgMethodNotAllowed    = "The requested HTTP method is not allowed"
	msgForbidden           = "You are not allowed to perform this action"
	msgConflict            = "The request conflicts with the current state of the resource"
	msgRateLimited         = "Too many requests"
	msgRequestTooLarge     = "The request body is too large"
	msgGatewayTimeout      = "The request timed out while waiting for a response"
	msgClientClosedRequest = "Client closed request"
	msgInvalidLogin        = "Invalid email or password"
	msgUnauthorized        = "Authentication is required"
)

// Error 是携带结构化错误码与可选字段级校验明细的业务错误。
type Error struct {
	code      Code
	message   string
	fields    map[string][]string
	fieldKeys map[string][]MessageKey
	cause     error
}

// Error 实现 error 接口，返回面向调用方的文案。
func (e *Error) Error() string { return e.message }

// Unwrap 暴露底层错误，使 errors.Is/As 可以继续向下匹配。
func (e *Error) Unwrap() error { return e.cause }

// Code 返回错误码。
func (e *Error) Code() Code { return e.code }

// Message 返回面向调用方的英文描述。
func (e *Error) Message() string { return e.message }

// Fields 返回按字段路径归集的校验明细，无明细时为 nil。
func (e *Error) Fields() map[string][]string { return e.fields }

// WithField 追加一条字段级明细并返回自身，供构造时链式调用。
func (e *Error) WithField(field, message string) *Error {
	if e.fields == nil {
		e.fields = make(map[string][]string)
	}
	e.fields[field] = append(e.fields[field], message)
	return e
}

// New 按错误码构造业务错误，并用 oops 捕获堆栈与来源域。
func New(code Code, message string) *Error {
	return &Error{
		code:    code,
		message: message,
		cause:   oops.Code(string(code)).In(domain(code)).Public(message).New(message),
	}
}

// Wrap 在既有错误之上附加错误码语义，同时保留原始错误。
func Wrap(code Code, message string, cause error) *Error {
	if cause == nil {
		return New(code, message)
	}
	return &Error{
		code:    code,
		message: message,
		cause:   oops.Code(string(code)).In(domain(code)).Public(message).Wrapf(cause, "%s", message),
	}
}

// BadRequest 构造 400，用于请求体无法解析等结构性错误。
func BadRequest(message string) *Error {
	return New(CodeBadRequest, cmp.Or(message, msgBadRequest))
}

// NotFound 构造 404。
func NotFound(message string) *Error {
	return New(CodeNotFound, cmp.Or(message, msgNotFound))
}

// MethodNotAllowed 构造 405。
func MethodNotAllowed(message string) *Error {
	return New(CodeMethodNotAllowed, cmp.Or(message, msgMethodNotAllowed))
}

// Forbidden 构造 403。
func Forbidden(message string) *Error {
	return New(CodeForbidden, cmp.Or(message, msgForbidden))
}

// Conflict 构造 409。
func Conflict(message string) *Error {
	return New(CodeConflict, cmp.Or(message, msgConflict))
}

// IdempotencyConflict 构造 409，表示同一幂等键正在执行中。
func IdempotencyConflict(message string) *Error {
	return New(CodeIdempotencyConflict, cmp.Or(message, "A request with the same idempotency key is currently being processed"))
}

// IdempotencyMismatch 构造 422，表示相同幂等键被复用于不同的请求载荷。
func IdempotencyMismatch(message string) *Error {
	return New(CodeIdempotencyMismatch, cmp.Or(message, "Idempotency key was previously used with different request parameters"))
}

// IdempotencyRequired 构造 400，表示当前端点强制要求携带幂等键。
func IdempotencyRequired(message string) *Error {
	return New(CodeIdempotencyRequired, cmp.Or(message, "Idempotency-Key header is required for this request"))
}

// RateLimited 构造 429。
func RateLimited(message string) *Error {
	return New(CodeRateLimited, cmp.Or(message, msgRateLimited))
}

// RequestTooLarge 构造 413，用于请求体超过中间件设定的上限。
func RequestTooLarge(cause error) *Error {
	return Wrap(CodeRequestTooLarge, msgRequestTooLarge, cause)
}

// GatewayTimeout 构造 504，用于请求在等待外部依赖或完成处理时超时。
func GatewayTimeout(cause error) *Error {
	return Wrap(CodeGatewayTimeout, msgGatewayTimeout, cause)
}

// ClientClosedRequest 构造 499，用于客户端在响应完成前主动终止连接。
func ClientClosedRequest(cause error) *Error {
	return Wrap(CodeClientClosedRequest, msgClientClosedRequest, cause)
}

// ServiceUnavailable 构造 503，用于外部依赖或本服务暂不可用。
func ServiceUnavailable(message string) *Error {
	return New(CodeServiceUnavailable, cmp.Or(message, "Service is temporarily unavailable"))
}

// InvalidCredentials 是登录失败的唯一对外口径，不区分"邮箱不存在"与"密码错误"。
func InvalidCredentials() *Error { return New(CodeAuthInvalidCredentials, msgInvalidLogin) }

// Unauthorized 构造 401，用于凭证缺失、格式错误、过期或已撤销。
func Unauthorized(code Code, message string) *Error { return New(code, message) }

// Internal 把未预期错误收敛为 500；原始错误只进日志，绝不进响应体。
func Internal(cause error) *Error { return Wrap(CodeInternal, msgInternal, cause) }

// Validation 构造 422，fields 为按字段路径归集的校验失败文案。
func Validation(fields map[string][]string) *Error {
	e := New(CodeValidationFailed, msgValidationFailed)
	e.fields = fields
	return e
}

// MessageKey 描述一条基于语言包键待渲染的字段级校验提示。
type MessageKey struct {
	// Key 语言包 validation 节中的键名。
	Key string
	// Param 模板参数，无参时留空。
	Param string
}

// ValidationKeys 构造 422 校验错误，字段明细交由中间件按客户端语言进行翻译。
func ValidationKeys(fields map[string][]MessageKey) *Error {
	e := New(CodeValidationFailed, msgValidationFailed)
	e.fieldKeys = fields
	return e
}

// FieldKeys 返回待翻译的字段级明细，无明细时为 nil。
func (e *Error) FieldKeys() map[string][]MessageKey { return e.fieldKeys }

// As 从错误链中提取 *Error，是 errors.AsType 的语义化封装。
func As(err error) (*Error, bool) {
	return errors.AsType[*Error](err)
}

// Stack 提取错误链中的 oops 堆栈文本，非 oops 错误返回空串。
func Stack(err error) string {
	if oopsErr, ok := oops.AsOops(err); ok {
		return oopsErr.Stacktrace()
	}
	return ""
}
