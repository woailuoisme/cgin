package payment

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/rs/zerolog"

	"cgin/pkg/logger"
)

// paymentLogPlaceholder 是 SDK 传入空日志参数时的占位消息。
const paymentLogPlaceholder = "gopay sdk log entry"

// xlogAdapter 把 zerolog 适配为 gopay 期望的 xlog.XLogger 接口。
//
// gopay 只通过 SetLogger 暴露日志注入点，接口仅接受可变参数，无法保留结构化字段能力，
// 因此这里统一把参数拼装为单条消息写入消息体。
// gopay 的请求/响应明细日志都在 Debug 级别并可能包含报文片段，生产环境直接丢弃 Debug，
// 不依赖运维把 LOG_LEVEL 调对。
type xlogAdapter struct {
	logger       *zerolog.Logger
	debugEnabled bool
}

// newXLogAdapter 创建 gopay 日志适配器。
// debugEnabled 为假时丢弃全部 Debug 级输出（生产环境直接丢弃报文级日志）。
func newXLogAdapter(l *zerolog.Logger, debugEnabled bool) xlogAdapter {
	sub := cmp.Or(l, logger.Nop()).With().Str("component", "payment_sdk").Logger()
	return xlogAdapter{
		logger:       &sub,
		debugEnabled: debugEnabled,
	}
}

// Debug 记录 Debug 级别日志；生产环境为丢弃。
func (a xlogAdapter) Debug(args ...any) {
	if a.debugEnabled {
		a.logger.Debug().Msg(composeLogMessage(args...))
	}
}

// Info 记录 Info 级别日志。
func (a xlogAdapter) Info(args ...any) { a.logger.Info().Msg(composeLogMessage(args...)) }

// Warn 记录 Warn 级别日志。
func (a xlogAdapter) Warn(args ...any) { a.logger.Warn().Msg(composeLogMessage(args...)) }

// Error 记录 Error 级别日志。
func (a xlogAdapter) Error(args ...any) { a.logger.Error().Msg(composeLogMessage(args...)) }

// Debugf 按格式记录 Debug 级别日志；生产环境为丢弃。
func (a xlogAdapter) Debugf(format string, args ...any) {
	if a.debugEnabled {
		a.logger.Debug().Msgf(format, args...)
	}
}

// Infof 按格式记录 Info 级别日志。
func (a xlogAdapter) Infof(format string, args ...any) { a.logger.Info().Msgf(format, args...) }

// Warnf 按格式记录 Warn 级别日志。
func (a xlogAdapter) Warnf(format string, args ...any) { a.logger.Warn().Msgf(format, args...) }

// Errorf 按格式记录 Error 级别日志。
func (a xlogAdapter) Errorf(format string, args ...any) { a.logger.Error().Msgf(format, args...) }

// composeLogMessage 把可变参数以空格分隔拼装为单条日志消息，空消息回退为占位符。
//
// gopay 的可变参数语义未在 SDK 中约定，统一按空格分隔可避免 fmt.Sprint 把
// 相邻字符串直接粘连成不可读的一坨。
func composeLogMessage(args ...any) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, fmt.Sprint(arg))
	}
	return cmp.Or(strings.TrimSpace(strings.Join(parts, " ")), paymentLogPlaceholder)
}
