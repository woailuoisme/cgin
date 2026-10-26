// Package log 提供用于开发调试环境的日志型短信驱动实现。
package log

import (
	"cmp"
	"context"

	"github.com/rs/zerolog"

	"cgin/pkg/logger"
	"cgin/pkg/notification/channel/sms"
)

// Driver 结构化日志短信驱动。
type Driver struct {
	logger *zerolog.Logger
}

var _ sms.Driver = (*Driver)(nil)

// NewDriver 创建日志短信驱动实例，未注入时回退全局默认日志器。
func NewDriver(logs ...*zerolog.Logger) *Driver {
	// 显式注入优先，其次回退全局默认日志器，二者皆空时由 Default 的 Nop 兜底。
	base := cmp.Or(append(logs, logger.Default())...)
	sub := base.With().Str("component", "sms_log_driver").Logger()
	return &Driver{logger: &sub}
}

// Name 返回驱动标识名。
func (d *Driver) Name() string {
	return "log"
}

// Send 记录短信发送的结构化日志。
func (d *Driver) Send(_ context.Context, msg *sms.Message) error {
	event := d.logger.Info().
		Str("phone", msg.PhoneNumber).
		Str("sign_name", msg.SignName).
		Str("template_code", msg.TemplateCode)
	if len(msg.TemplateParam) > 0 {
		event = event.Any("params", msg.TemplateParam)
	}
	if msg.Content != "" {
		event = event.Str("content", msg.Content)
	}
	event.Msg("sms notification logged via log driver")
	return nil
}
