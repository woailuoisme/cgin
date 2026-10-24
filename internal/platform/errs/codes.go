// Package errs 定义统一的错误码、错误类型与 HTTP 映射。
package errs

import (
	"net/http"
	"slices"
	"strings"
)

// Code 是写入响应体 code 字段的机器可读错误码。
type Code string

// StatusClientClosedRequest 是客户端主动断开连接的状态码（499）。
const StatusClientClosedRequest = 499

// 通用错误码。这些值必须与 api.md 5.2 的描述保持一致。
const (
	CodeBadRequest       Code = "BAD_REQUEST"
	CodeValidationFailed Code = "VALIDATION_FAILED"
	CodeNotFound         Code = "NOT_FOUND"
	CodeMethodNotAllowed Code = "METHOD_NOT_ALLOWED"
	CodeForbidden        Code = "FORBIDDEN"
	CodeConflict         Code = "CONFLICT"
	CodeRateLimited      Code = "RATE_LIMITED"
	// 幂等性控制错误码。
	CodeIdempotencyConflict Code = "IDEMPOTENCY_CONFLICT"
	CodeIdempotencyMismatch Code = "IDEMPOTENCY_MISMATCH"
	CodeIdempotencyRequired Code = "IDEMPOTENCY_REQUIRED"
	// CodeRequestTooLarge 请求体超过服务端上限。
	CodeRequestTooLarge Code = "REQUEST_TOO_LARGE"
	// CodeGatewayTimeout 上游依赖或数据库未在时限内响应。
	CodeGatewayTimeout Code = "GATEWAY_TIMEOUT"
	// CodeClientClosedRequest 客户端在服务端响应完成前主动终止了连接。
	CodeClientClosedRequest Code = "CLIENT_CLOSED_REQUEST"
	CodeServiceUnavailable  Code = "SERVICE_UNAVAILABLE"
	CodeInternal            Code = "INTERNAL_SERVER_ERROR"
)

// 认证域错误码。前缀 AUTH_ 用于把认证失败与业务失败区分开。
const (
	CodeAuthInvalidCredentials Code = "AUTH_INVALID_CREDENTIALS"
	CodeAuthEmailAlreadyExists Code = "AUTH_EMAIL_ALREADY_EXISTS"
	CodeAuthUsernameTaken      Code = "AUTH_USERNAME_TAKEN"
	CodeAuthPhoneNumberTaken   Code = "AUTH_PHONE_NUMBER_TAKEN"
	CodeAuthAccountBanned      Code = "AUTH_ACCOUNT_BANNED"
	CodeAuthEmailNotVerified   Code = "AUTH_EMAIL_NOT_VERIFIED"
	CodeAuthOTPInvalid         Code = "AUTH_OTP_INVALID"
	CodeAuthOTPExpired         Code = "AUTH_OTP_EXPIRED"
	CodeAuthOTPTooManyAttempts Code = "AUTH_OTP_TOO_MANY_ATTEMPTS"
	CodeAuthOTPRateLimited     Code = "AUTH_OTP_RATE_LIMITED"
	CodeAuthTokenInvalid       Code = "AUTH_TOKEN_INVALID"
	CodeAuthTokenExpired       Code = "AUTH_TOKEN_EXPIRED"
	CodeAuthTokenRevoked       Code = "AUTH_TOKEN_REVOKED"
	CodeAuthPasswordMismatch   Code = "AUTH_PASSWORD_MISMATCH"
	CodeAuthPasswordTooWeak    Code = "AUTH_PASSWORD_TOO_WEAK"
)

// 子系统错误码。
//
// 命名与通用码、认证域码一致：一律 SHOUTY_SNAKE_CASE，且只在本文件声明，
// 任何抛出点都必须引用这里的常量，不得再写裸字符串字面量。
//
// 分两级：
//   - 已在 statuses 登记的（excel / notification / payment 的客户端可纠正失败、依赖不可用）：
//     响应体直接带上该码与对应状态码；
//   - 未登记的（内部失败：驱动未配置、第三方 SDK 报错、程序错误）：一律按 500 处理，
//     响应体固定为 INTERNAL_SERVER_ERROR，原始码只出现在日志与堆栈里，不对外扩散。
const (
	// 导入导出子系统：客户端提供的文件或行数据有问题（已登记）。
	CodeExcelOpenError    Code = "EXCEL_OPEN_ERROR"
	CodeExcelReadError    Code = "EXCEL_READ_ERROR"
	CodeExcelFileTooLarge Code = "EXCEL_FILE_TOO_LARGE"
	CodeExcelMapError     Code = "EXCEL_MAP_ERROR"
	CodeExcelRowsError    Code = "EXCEL_ROWS_ERROR"
	CodeEmptySheets       Code = "EMPTY_SHEETS"
	// CodeCSVReadError CSV 输入流不合法或无法读取。
	CodeCSVReadError Code = "CSV_READ_ERROR"
	// CodeExcelReadRowError 逐行读取时该行内容不合法。
	CodeExcelReadRowError Code = "EXCEL_READ_ROW_ERROR"
	// CodeExcelDuplicateRow 导入数据行存在重复键（文件内重复或数据库已存在）。
	CodeExcelDuplicateRow Code = "EXCEL_DUPLICATE_ROW"

	// 通知子系统：接收方缺失或渠道不支持（已登记）。
	CodeNotificationMissingRecipient   Code = "NOTIFICATION_MISSING_RECIPIENT"
	CodeNotificationMissingRecipientID Code = "NOTIFICATION_MISSING_RECIPIENT_ID"
	CodeNotificationUnsupportedSMS     Code = "NOTIFICATION_UNSUPPORTED_SMS"
	CodeNotificationUnsupportedMail    Code = "NOTIFICATION_UNSUPPORTED_MAIL"
	CodeNotificationUnsupportedDB      Code = "NOTIFICATION_UNSUPPORTED_DATABASE"
	CodeNotificationUnsupportedRT      Code = "NOTIFICATION_UNSUPPORTED_REALTIME"
	CodeNotificationUnsupportedGotify  Code = "NOTIFICATION_UNSUPPORTED_GOTIFY"
	CodeNotificationUnsupportedChat    Code = "NOTIFICATION_UNSUPPORTED_CHAT"
	CodeNotificationChannelNotFound    Code = "NOTIFICATION_CHANNEL_NOT_FOUND"
	CodeNotificationRateLimited        Code = "NOTIFICATION_RATE_LIMITED"
	CodeNotificationDeduplicated       Code = "NOTIFICATION_DEDUPLICATED"
	CodeMailMissingRecipient           Code = "MAIL_MISSING_RECIPIENT"
	CodeSMSMissingPhone                Code = "SMS_MISSING_PHONE"
	CodeInvalidSMSMessage              Code = "INVALID_SMS_MESSAGE"
	CodeInvalidGotifyMessage           Code = "INVALID_GOTIFY_MESSAGE"
	CodeInvalidChatMessage             Code = "INVALID_CHAT_MESSAGE"

	// 支付子系统（已登记）。
	CodePaymentCapabilityNotSupported Code = "PAYMENT_CAPABILITY_NOT_SUPPORTED"

	// 基础设施依赖不可用，可重试（已登记）。
	CodeDatabaseUnavailable Code = "DATABASE_UNAVAILABLE"

	// 以下为内部失败码：不登记进 statuses，对外统一 500，仅用于堆栈与日志定位。
	CodeCSVWriteError            Code = "CSV_WRITE_ERROR"
	CodeCSVFlushError            Code = "CSV_FLUSH_ERROR"
	CodeCSVGeneratorError        Code = "CSV_GENERATOR_ERROR"
	CodeExcelStreamError         Code = "EXCEL_STREAM_ERROR"
	CodeExcelStreamWriteError    Code = "EXCEL_STREAM_WRITE_ERROR"
	CodeExcelFlushError          Code = "EXCEL_FLUSH_ERROR"
	CodeExcelStyleError          Code = "EXCEL_STYLE_ERROR"
	CodeExcelGeneratorError      Code = "EXCEL_GENERATOR_ERROR"
	CodeExcelConsumeError        Code = "EXCEL_CONSUME_ERROR"
	CodeExcelTempFileError       Code = "EXCEL_TEMP_FILE_ERROR"
	CodeExcelSpoolWriteError     Code = "EXCEL_SPOOL_WRITE_ERROR"
	CodeExcelSpoolCopyError      Code = "EXCEL_SPOOL_COPY_ERROR"
	CodeExcelSpoolCloseError     Code = "EXCEL_SPOOL_CLOSE_ERROR"
	CodeExcelNilPointer          Code = "EXCEL_NIL_POINTER"
	CodeNilWriter                Code = "NIL_WRITER"
	CodeNilReader                Code = "NIL_READER"
	CodeNilGenerator             Code = "NIL_GENERATOR"
	CodeNilCallback              Code = "NIL_CALLBACK"
	CodeContextCanceled          Code = "CONTEXT_CANCELED"
	CodeNotificationSendFailed   Code = "NOTIFICATION_SEND_FAILED"
	CodeNotificationFormatFailed Code = "NOTIFICATION_FORMAT_FAILED"
	CodeNotificationEncodeError  Code = "NOTIFICATION_ENCODE_ERROR"
	CodeNotificationPanic        Code = "NOTIFICATION_PANIC"
	CodeMailDriverNotFound       Code = "MAIL_DRIVER_NOT_FOUND"
	CodeSMSDriverNotFound        Code = "SMS_DRIVER_NOT_FOUND"
	CodeGotifyDriverNotFound     Code = "GOTIFY_DRIVER_NOT_FOUND"
	CodeChatDriverNotFound       Code = "CHAT_DRIVER_NOT_FOUND"
	CodeGotifyAPIError           Code = "GOTIFY_API_ERROR"
	CodeTelegramAPIError         Code = "TELEGRAM_API_ERROR"
	CodeAliyunHTTPError          Code = "ALIYUN_HTTP_ERROR"
	CodeAliyunAPIError           Code = "ALIYUN_API_ERROR"
	CodeTencentHTTPError         Code = "TENCENT_HTTP_ERROR"
	CodeTencentAPIError          Code = "TENCENT_API_ERROR"
	CodeTencentDeliveryFailed    Code = "TENCENT_DELIVERY_FAILED"
	CodeSMSBaoSendFailed         Code = "SMSBAO_SEND_FAILED"
	CodeInvalidPaymentConfig     Code = "INVALID_PAYMENT_CONFIG"
	CodePaymentDriverNotFound    Code = "PAYMENT_DRIVER_NOT_FOUND"
)

// ExposedCodes 返回已在 statuses 登记、会直接出现在响应体 code 字段里的子系统错误码，
// 供 api.md 契约测试核对清单是否漂移。
func ExposedCodes() []Code {
	codes := make([]Code, 0, len(statuses))
	for c := range statuses {
		codes = append(codes, c)
	}
	slices.Sort(codes)
	return codes
}

// statuses 是错误码到 HTTP 状态码的唯一映射表，新增错误码必须在此登记。
var statuses = map[Code]int{
	CodeBadRequest:          http.StatusBadRequest,
	CodeValidationFailed:    http.StatusUnprocessableEntity,
	CodeNotFound:            http.StatusNotFound,
	CodeMethodNotAllowed:    http.StatusMethodNotAllowed,
	CodeForbidden:           http.StatusForbidden,
	CodeConflict:            http.StatusConflict,
	CodeRateLimited:         http.StatusTooManyRequests,
	CodeIdempotencyConflict: http.StatusConflict,
	CodeIdempotencyMismatch: http.StatusUnprocessableEntity,
	CodeIdempotencyRequired: http.StatusBadRequest,
	CodeRequestTooLarge:     http.StatusRequestEntityTooLarge,
	CodeGatewayTimeout:      http.StatusGatewayTimeout,
	CodeClientClosedRequest: StatusClientClosedRequest,
	CodeServiceUnavailable:  http.StatusServiceUnavailable,
	CodeInternal:            http.StatusInternalServerError,

	CodeAuthInvalidCredentials: http.StatusUnauthorized,
	CodeAuthEmailAlreadyExists: http.StatusConflict,
	CodeAuthUsernameTaken:      http.StatusConflict,
	CodeAuthPhoneNumberTaken:   http.StatusConflict,
	CodeAuthAccountBanned:      http.StatusForbidden,
	CodeAuthEmailNotVerified:   http.StatusForbidden,
	CodeAuthOTPInvalid:         http.StatusUnprocessableEntity,
	CodeAuthOTPExpired:         http.StatusUnprocessableEntity,
	CodeAuthOTPTooManyAttempts: http.StatusTooManyRequests,
	CodeAuthOTPRateLimited:     http.StatusTooManyRequests,
	CodeAuthTokenInvalid:       http.StatusUnauthorized,
	CodeAuthTokenExpired:       http.StatusUnauthorized,
	CodeAuthTokenRevoked:       http.StatusUnauthorized,
	CodeAuthPasswordMismatch:   http.StatusUnprocessableEntity,
	CodeAuthPasswordTooWeak:    http.StatusUnprocessableEntity,

	CodeExcelOpenError:    http.StatusBadRequest,
	CodeExcelReadError:    http.StatusBadRequest,
	CodeExcelFileTooLarge: http.StatusRequestEntityTooLarge,
	CodeExcelMapError:     http.StatusUnprocessableEntity,
	CodeExcelRowsError:    http.StatusUnprocessableEntity,
	CodeExcelReadRowError: http.StatusUnprocessableEntity,
	CodeExcelDuplicateRow: http.StatusConflict,
	CodeEmptySheets:       http.StatusBadRequest,
	CodeCSVReadError:      http.StatusBadRequest,

	CodeNotificationMissingRecipient:   http.StatusBadRequest,
	CodeNotificationMissingRecipientID: http.StatusBadRequest,
	CodeNotificationUnsupportedSMS:     http.StatusBadRequest,
	CodeNotificationUnsupportedMail:    http.StatusBadRequest,
	CodeNotificationUnsupportedDB:      http.StatusBadRequest,
	CodeNotificationUnsupportedRT:      http.StatusBadRequest,
	CodeNotificationUnsupportedGotify:  http.StatusBadRequest,
	CodeNotificationUnsupportedChat:    http.StatusBadRequest,
	CodeNotificationChannelNotFound:    http.StatusBadRequest,
	CodeNotificationRateLimited:        http.StatusTooManyRequests,
	CodeNotificationDeduplicated:       http.StatusConflict,
	CodeMailMissingRecipient:           http.StatusBadRequest,
	CodeSMSMissingPhone:                http.StatusBadRequest,
	CodeInvalidSMSMessage:              http.StatusBadRequest,
	CodeInvalidGotifyMessage:           http.StatusBadRequest,
	CodeInvalidChatMessage:             http.StatusBadRequest,

	CodePaymentCapabilityNotSupported: http.StatusBadRequest,

	CodeDatabaseUnavailable: http.StatusServiceUnavailable,
}

// Status 返回错误码对应的 HTTP 状态码；未登记的错误码一律按 500 处理。
func Status(c Code) int {
	if s, ok := statuses[c]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// statusMessages 是各状态码在错误未声明对外文案时的兜底描述。
// 4xx 必须给出与状态语义相符的说明，不能沿用 5xx 的"意外错误"口吻。
var statusMessages = map[int]string{
	http.StatusBadRequest:            msgBadRequest,
	http.StatusUnauthorized:          msgUnauthorized,
	http.StatusForbidden:             msgForbidden,
	http.StatusNotFound:              msgNotFound,
	http.StatusMethodNotAllowed:      msgMethodNotAllowed,
	http.StatusConflict:              msgConflict,
	http.StatusRequestEntityTooLarge: msgRequestTooLarge,
	http.StatusUnprocessableEntity:   msgValidationFailed,
	http.StatusTooManyRequests:       msgRateLimited,
	StatusClientClosedRequest:        msgClientClosedRequest,
	http.StatusGatewayTimeout:        msgGatewayTimeout,
	http.StatusServiceUnavailable:    "Service is temporarily unavailable",
	http.StatusInternalServerError:   msgInternal,
}

// MessageForStatus 返回状态码对应的兜底对外文案，未覆盖的状态一律按 500 文案处理。
func MessageForStatus(status int) string {
	if msg, ok := statusMessages[status]; ok {
		return msg
	}
	return msgInternal
}

// domain 用于给 oops 标注错误来源，便于日志按子系统筛选。
func domain(c Code) string {
	if strings.HasPrefix(string(c), "AUTH_") {
		return "auth"
	}
	return "app"
}
