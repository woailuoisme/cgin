package sentry

import (
	"reflect"
	"slices"

	"github.com/getsentry/sentry-go"
	"go.uber.org/zap/zapcore"
)

// zapLevelToSentry 将 zapcore.Level 映射为 sentry.Level。
func zapLevelToSentry(l zapcore.Level) sentry.Level {
	switch l {
	case zapcore.DebugLevel:
		return sentry.LevelDebug
	case zapcore.InfoLevel:
		return sentry.LevelInfo
	case zapcore.WarnLevel:
		return sentry.LevelWarning
	case zapcore.ErrorLevel:
		return sentry.LevelError
	case zapcore.DPanicLevel, zapcore.PanicLevel, zapcore.FatalLevel:
		return sentry.LevelFatal
	default:
		return sentry.LevelInfo
	}
}

// fieldsToMap 将 zapcore.Field 列表转换为 map[string]any 字典。
func fieldsToMap(fields []zapcore.Field) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range fields {
		f.AddTo(enc)
	}
	return enc.Fields
}

// zapCore 实现 zapcore.Core 接口，将 Zap 日志同步至 Sentry / GlitchTip。
type zapCore struct {
	minLevel zapcore.Level
	fields   []zapcore.Field
}

// NewZapCore 构建用于拦截 Warn（记录面包屑）与 Error/Fatal（上报事件）的 Sentry Zap 核心。
func NewZapCore(minLevel zapcore.Level) zapcore.Core {
	return &zapCore{
		minLevel: minLevel,
	}
}

func (c *zapCore) Enabled(lvl zapcore.Level) bool {
	return IsInitialized() && lvl >= c.minLevel
}

func (c *zapCore) With(fields []zapcore.Field) zapcore.Core {
	return &zapCore{
		minLevel: c.minLevel,
		fields:   slices.Concat(c.fields, fields),
	}
}

func (c *zapCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *zapCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	if !IsInitialized() {
		return nil
	}

	allFields := slices.Concat(c.fields, fields)
	fieldsMap := fieldsToMap(allFields)

	// Warn 级别转化为 Sentry 面包屑，不生成独立 Issue
	if ent.Level == zapcore.WarnLevel {
		breadcrumb := &sentry.Breadcrumb{
			Type:      "default",
			Category:  ent.LoggerName,
			Message:   ent.Message,
			Level:     sentry.LevelWarning,
			Timestamp: ent.Time,
			Data:      fieldsMap,
		}
		sentry.AddBreadcrumb(breadcrumb)
		return nil
	}

	// Error 及以上级别构建 Sentry 事件并上报
	if ent.Level >= zapcore.ErrorLevel {
		c.captureErrorEntry(ent, allFields, fieldsMap)
	}

	return nil
}

// captureErrorEntry 构建并发送 Sentry 错误事件。
func (c *zapCore) captureErrorEntry(ent zapcore.Entry, allFields []zapcore.Field, fieldsMap map[string]any) {
	event := sentry.NewEvent()
	event.Message = ent.Message
	event.Level = zapLevelToSentry(ent.Level)
	event.Timestamp = ent.Time
	event.Logger = ent.LoggerName
	if len(fieldsMap) > 0 {
		event.Contexts["extra"] = fieldsMap
	}

	// 提取字段中的实际 error 对象
	for _, f := range allFields {
		if f.Type != zapcore.ErrorType || f.Interface == nil {
			continue
		}
		err, ok := f.Interface.(error)
		if !ok {
			continue
		}
		event.Exception = append(event.Exception, sentry.Exception{
			Value:      err.Error(),
			Type:       reflect.TypeOf(err).String(),
			Stacktrace: sentry.ExtractStacktrace(err),
		})
	}

	// 若未从 error 中解析出堆栈，则为当前调用生成堆栈
	if len(event.Exception) == 0 {
		event.Threads = []sentry.Thread{{
			Stacktrace: sentry.NewStacktrace(),
			Current:    true,
		}}
	}

	sentry.CaptureEvent(event)
}

func (c *zapCore) Sync() error {
	return nil
}
