// Package eventbus 提供基于 Watermill CQRS 的事件总线与消息路由基础设施。
package eventbus

import (
	"cmp"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/rs/zerolog"

	"cgin/pkg/logger"
)

// LoggerAdapter 将系统统一的 *zerolog.Logger 适配为 Watermill 的 LoggerAdapter 接口。
type LoggerAdapter struct {
	logger *zerolog.Logger
}

var _ watermill.LoggerAdapter = (*LoggerAdapter)(nil)

// NewLoggerAdapter 创建 Watermill 日志适配器，nil 日志器自动降级为静默输出。
func NewLoggerAdapter(l *zerolog.Logger) *LoggerAdapter {
	return &LoggerAdapter{logger: cmp.Or(l, logger.Nop())}
}

// Error 记录错误级别日志。
func (a *LoggerAdapter) Error(msg string, err error, fields watermill.LogFields) {
	a.logger.Error().Err(err).Fields(fields).Msg(msg)
}

// Info 记录信息级别日志。
func (a *LoggerAdapter) Info(msg string, fields watermill.LogFields) {
	a.logger.Info().Fields(fields).Msg(msg)
}

// Debug 记录调试级别日志。
func (a *LoggerAdapter) Debug(msg string, fields watermill.LogFields) {
	a.logger.Debug().Fields(fields).Msg(msg)
}

// Trace 记录跟踪级别日志。
func (a *LoggerAdapter) Trace(msg string, fields watermill.LogFields) {
	a.logger.Trace().Fields(fields).Msg(msg)
}

// With 返回携带指定上下文字段的新适配器实例，字段下沉至日志器上下文而非逐条重复写入。
func (a *LoggerAdapter) With(fields watermill.LogFields) watermill.LoggerAdapter {
	if len(fields) == 0 {
		return a
	}
	sub := a.logger.With().Fields(fields).Logger()
	return &LoggerAdapter{logger: &sub}
}
