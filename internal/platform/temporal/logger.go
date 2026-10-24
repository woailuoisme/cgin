package temporal

import (
	"cmp"

	temporalLog "go.temporal.io/sdk/log"
	"go.uber.org/zap"
)

// zapAdapter 实现 Temporal SDK 的 log.Logger 接口，将日志输出重定向至 Zap。
type zapAdapter struct {
	logger *zap.SugaredLogger
}

// NewZapAdapter 构造适配 Temporal SDK 的 Zap 日志记录器。
func NewZapAdapter(logger *zap.Logger) temporalLog.Logger {
	return &zapAdapter{
		logger: cmp.Or(logger, zap.NewNop()).Named("temporal").Sugar(),
	}
}

func (z *zapAdapter) Debug(msg string, keyvals ...any) {
	z.logger.Debugw(msg, keyvals...)
}

func (z *zapAdapter) Info(msg string, keyvals ...any) {
	z.logger.Infow(msg, keyvals...)
}

func (z *zapAdapter) Warn(msg string, keyvals ...any) {
	z.logger.Warnw(msg, keyvals...)
}

func (z *zapAdapter) Error(msg string, keyvals ...any) {
	z.logger.Errorw(msg, keyvals...)
}
