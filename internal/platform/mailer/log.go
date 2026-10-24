package mailer

import (
	"cmp"
	"context"

	"go.uber.org/zap"
)

// LogMailer 将邮件投递记录写入结构化日志，不发起任何真实外部网络调用。
type LogMailer struct {
	log *zap.Logger
}

var _ Mailer = (*LogMailer)(nil)

// NewLogMailer 创建结构化日志邮件器实例。
func NewLogMailer(log *zap.Logger) *LogMailer {
	return &LogMailer{log: cmp.Or(log, zap.NewNop())}
}

// Send 将邮件元数据与正文以结构化日志格式输出。
func (l *LogMailer) Send(ctx context.Context, msg Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	l.log.Info("mail sending simulated via log driver",
		zap.String("to", msg.To),
		zap.Strings("cc", msg.Cc),
		zap.Strings("bcc", msg.Bcc),
		zap.String("subject", msg.Subject),
		zap.Int("body_length", len(msg.Body)),
		zap.Bool("is_html", msg.IsHTML),
		zap.Int("attachments", len(msg.AllAttachments())),
	)
	return nil
}

// SendMailable 渲染结构化模板邮件并记录到日志。
func (l *LogMailer) SendMailable(ctx context.Context, to string, mailable Mailable) error {
	return sendMailable(ctx, l, to, mailable)
}

// Close 释放日志邮件器资源。
func (l *LogMailer) Close() error {
	return nil
}

// Ping 检查日志发信器可用性。
func (l *LogMailer) Ping(ctx context.Context) error {
	return ctx.Err()
}

// Driver 返回日志驱动标识。
func (l *LogMailer) Driver() string {
	return "log"
}

// Enabled 报告日志发信驱动处于启用状态。
func (l *LogMailer) Enabled() bool {
	return true
}

// NullMailer 静默丢弃所有外发邮件，专用于压测或完全屏蔽邮件输出的场景。
type NullMailer struct{}

var _ Mailer = (*NullMailer)(nil)

// NewNullMailer 创建空邮件器实例。
func NewNullMailer() *NullMailer {
	return &NullMailer{}
}

// Send 静默忽略邮件投递。
func (n *NullMailer) Send(ctx context.Context, _ Message) error {
	return ctx.Err()
}

// SendMailable 静默忽略模板邮件投递。
func (n *NullMailer) SendMailable(ctx context.Context, _ string, _ Mailable) error {
	return ctx.Err()
}

// Close 释放空邮件器资源。
func (n *NullMailer) Close() error {
	return nil
}

// Ping 检查空发信器可用性。
func (n *NullMailer) Ping(ctx context.Context) error {
	return ctx.Err()
}

// Driver 返回空发信驱动标识。
func (n *NullMailer) Driver() string {
	return "null"
}

// Enabled 报告空发信驱动处于启用状态。
func (n *NullMailer) Enabled() bool {
	return true
}
