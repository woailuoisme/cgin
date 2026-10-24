// Package log 提供用于开发调试环境的日志型短信驱动实现。
package log

import (
	"context"

	"go.uber.org/zap"

	"bgin/internal/platform/logger"
	"bgin/internal/platform/notification/channel/sms"
)

// Driver 结构化日志短信驱动。
type Driver struct {
	logger *zap.Logger
}

var _ sms.Driver = (*Driver)(nil)

// NewDriver 创建日志短信驱动实例。
func NewDriver(l ...*zap.Logger) *Driver {
	var lg *zap.Logger
	if len(l) > 0 && l[0] != nil {
		lg = l[0]
	} else {
		lg = logger.FromContext(context.Background())
	}
	return &Driver{
		logger: lg.With(zap.String("component", "sms_log_driver")),
	}
}

// Name 返回驱动标识名。
func (d *Driver) Name() string {
	return "log"
}

// Send 记录短信发送的结构化日志。
func (d *Driver) Send(_ context.Context, msg *sms.Message) error {
	fields := []zap.Field{
		zap.String("phone", msg.PhoneNumber),
		zap.String("sign_name", msg.SignName),
		zap.String("template_code", msg.TemplateCode),
	}

	if len(msg.TemplateParam) > 0 {
		fields = append(fields, zap.Any("params", msg.TemplateParam))
	}
	if msg.Content != "" {
		fields = append(fields, zap.String("content", msg.Content))
	}

	d.logger.Info("sms notification logged via log driver", fields...)
	return nil
}
