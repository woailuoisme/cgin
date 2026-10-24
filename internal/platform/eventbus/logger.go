// Package eventbus 提供基于 Watermill CQRS 的事件总线与消息路由基础设施。
package eventbus

import (
	"maps"

	"github.com/ThreeDotsLabs/watermill"
	"go.uber.org/zap"
)

// ZapLoggerAdapter 将系统统一的 *zap.Logger 适配为 Watermill 的 LoggerAdapter 接口。
type ZapLoggerAdapter struct {
	logger *zap.Logger
	fields watermill.LogFields
}

// NewZapLoggerAdapter 创建一个新的 ZapLoggerAdapter 实例。
func NewZapLoggerAdapter(logger *zap.Logger) *ZapLoggerAdapter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ZapLoggerAdapter{
		logger: logger,
	}
}

func (a *ZapLoggerAdapter) toZapFields(fields watermill.LogFields, err error) []zap.Field {
	count := len(a.fields) + len(fields)
	if err != nil {
		count++
	}
	if count == 0 {
		return nil
	}
	out := make([]zap.Field, 0, count)
	if err != nil {
		out = append(out, zap.Error(err))
	}
	for k, v := range a.fields {
		out = append(out, zap.Any(k, v))
	}
	for k, v := range fields {
		out = append(out, zap.Any(k, v))
	}
	return out
}

// Error 记录错误级别日志。
func (a *ZapLoggerAdapter) Error(msg string, err error, fields watermill.LogFields) {
	a.logger.Error(msg, a.toZapFields(fields, err)...)
}

// Info 记录信息级别日志。
func (a *ZapLoggerAdapter) Info(msg string, fields watermill.LogFields) {
	a.logger.Info(msg, a.toZapFields(fields, nil)...)
}

// Debug 记录调试级别日志。
func (a *ZapLoggerAdapter) Debug(msg string, fields watermill.LogFields) {
	a.logger.Debug(msg, a.toZapFields(fields, nil)...)
}

// Trace 记录跟踪级别日志。
func (a *ZapLoggerAdapter) Trace(msg string, fields watermill.LogFields) {
	a.logger.Debug(msg, a.toZapFields(fields, nil)...)
}

// With 返回携带指定上下文字段的新适配器实例。
func (a *ZapLoggerAdapter) With(fields watermill.LogFields) watermill.LoggerAdapter {
	if len(fields) == 0 {
		return a
	}
	combined := make(watermill.LogFields, len(a.fields)+len(fields))
	maps.Copy(combined, a.fields)
	maps.Copy(combined, fields)
	return &ZapLoggerAdapter{
		logger: a.logger,
		fields: combined,
	}
}
