package payment

import (
	"cmp"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// paymentLogPlaceholder 是 SDK 传入空日志参数时的占位消息。
const paymentLogPlaceholder = "gopay sdk log entry"

// xlogAdapter 把 zap 适配为 gopay 期望的 xlog.XLogger 接口。
//
// gopay 只通过 SetLogger 暴露日志注入点，接口仅接受可变参数，无法保留结构化字段能力，
// 因此这里统一把参数拼装为单条消息转发给 SugaredLogger。
// gopay 的请求/响应明细日志都在 Debug 级别并可能包含报文片段，生产环境直接丢弃 Debug，
// 不依赖运维把 LOG_LEVEL 调对。
type xlogAdapter struct {
	sugar        *zap.SugaredLogger
	debugEnabled bool
}

// newXLogAdapter 创建 gopay 日志适配器。
// debugEnabled /* 是否保留 SDK Debug 明细（生产传 false，直接丢弃报文级输出） */ 为假时丢弃全部 Debug 级输出。
func newXLogAdapter(logger *zap.Logger, debugEnabled bool) xlogAdapter {
	logger = cmp.Or(logger, zap.NewNop())
	return xlogAdapter{
		sugar:        logger.With(zap.String("component", "payment_sdk")).Sugar(),
		debugEnabled: debugEnabled,
	}
}

// Debug 记录 Debug 级别日志；生产环境为丢弃。
func (a xlogAdapter) Debug(args ...any) {
	if !a.debugEnabled {
		return
	}
	a.sugar.Debug(composeLogMessage(args...))
}

// Info 记录 Info 级别日志。
func (a xlogAdapter) Info(args ...any) { a.sugar.Info(composeLogMessage(args...)) }

// Warn 记录 Warn 级别日志。
func (a xlogAdapter) Warn(args ...any) { a.sugar.Warn(composeLogMessage(args...)) }

// Error 记录 Error 级别日志。
func (a xlogAdapter) Error(args ...any) { a.sugar.Error(composeLogMessage(args...)) }

// Debugf 按格式记录 Debug 级别日志；生产环境为丢弃。
func (a xlogAdapter) Debugf(format string, args ...any) {
	if !a.debugEnabled {
		return
	}
	a.sugar.Debugf(format, args...)
}

// Infof 按格式记录 Info 级别日志。
func (a xlogAdapter) Infof(format string, args ...any) { a.sugar.Infof(format, args...) }

// Warnf 按格式记录 Warn 级别日志。
func (a xlogAdapter) Warnf(format string, args ...any) { a.sugar.Warnf(format, args...) }

// Errorf 按格式记录 Error 级别日志。
func (a xlogAdapter) Errorf(format string, args ...any) { a.sugar.Errorf(format, args...) }

// composeLogMessage 把可变参数拼装为单条日志消息，空消息回退为占位符。
func composeLogMessage(args ...any) string {
	return cmp.Or(strings.TrimSpace(fmt.Sprint(args...)), paymentLogPlaceholder)
}
