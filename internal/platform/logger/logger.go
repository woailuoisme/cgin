// Package logger 提供基于 zap 的高性能结构化日志器构建与请求作用域透传。
package logger

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"

	"bgin/internal/config"
)

// ctxKey 是日志器在 context 中的私有键，避免与其他包产生键名冲突。
type ctxKey struct{}

var (
	defaultLoggerMu sync.RWMutex
	defaultLogger   = newFallbackLogger()
)

// newFallbackLogger 创建系统启动早期或无配置时的兜底输出日志器。
func newFallbackLogger() *zap.Logger {
	encCfg := zap.NewDevelopmentEncoderConfig()
	encCfg.TimeKey = "timestamp"
	encCfg.EncodeTime = zapcore.RFC3339TimeEncoder
	encCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encCfg),
		zapcore.AddSync(os.Stderr),
		zapcore.ErrorLevel,
	)
	return zap.New(core)
}

// setDefault 在写锁保护下替换兜底日志器。
func setDefault(l *zap.Logger) {
	if l == nil {
		return
	}
	defaultLoggerMu.Lock()
	defaultLogger = l
	defaultLoggerMu.Unlock()
}

// defaultLog 在读锁保护下取得兜底日志器。
func defaultLog() *zap.Logger {
	defaultLoggerMu.RLock()
	defer defaultLoggerMu.RUnlock()
	return defaultLogger
}

// DefaultRedactKeys 预设的敏感字段键名列表（全小写存储，按键名精确匹配）。
// 除凭证外，手机号、邮箱、一次性验证码与账号标识同属个人敏感信息，一律脱敏。
var DefaultRedactKeys = []string{
	"password",
	"token",
	"secret",
	"authorization",
	"cookie",
	"access_token",
	"refresh_token",
	"api_key",
	"email",
	"phone",
	"phone_number",
	"otp",
	"identifier",
}

// Options 封装日志器的通用构建参数，遵循依赖倒置原则。
type Options struct {
	Level       string
	Channel     string
	ServiceName string
	Env         string
	Writer      io.Writer
	NoColor     *bool
	Redact      bool
	RedactKeys  []string
	AddCaller   bool
	ExtraCores  []zapcore.Core
}

// Option 定义日志器构建的函数式选项。
type Option func(*Options)

// WithLevel 设置最低日志级别（"debug", "info", "warn", "error"）。
func WithLevel(level string) Option {
	return func(o *Options) {
		o.Level = level
	}
}

// WithChannel 设置输出通道格式（"console" 或 "json"）。
func WithChannel(channel string) Option {
	return func(o *Options) {
		o.Channel = channel
	}
}

// WithServiceName 设置 JSON 日志中的 service 服务标识字段。
func WithServiceName(name string) Option {
	return func(o *Options) {
		o.ServiceName = name
	}
}

// WithWriter 注入自定义日志输出写入目标（便于单元测试捕获断言或写入外部管道）。
func WithWriter(w io.Writer) Option {
	return func(o *Options) {
		o.Writer = w
	}
}

// WithColor 显式指定是否开启 ANSI 着色。
func WithColor(enabled bool) Option {
	return func(o *Options) {
		noColor := !enabled
		o.NoColor = &noColor
	}
}

// WithRedaction 设置是否启用敏感数据脱敏过滤器，并可选覆盖敏感字段键名列表。
func WithRedaction(enabled bool, customKeys ...string) Option {
	return func(o *Options) {
		o.Redact = enabled
		if len(customKeys) > 0 {
			o.RedactKeys = customKeys
		}
	}
}

// WithCaller 设置是否开启调用方位置（文件名与行号）记录。
func WithCaller(enabled bool) Option {
	return func(o *Options) {
		o.AddCaller = enabled
	}
}

// WithExtraCore 附加自定义 zapcore.Core 输出目标（例如用于 Sentry/GlitchTip 事件上报）。
func WithExtraCore(c zapcore.Core) Option {
	return func(o *Options) {
		if c != nil {
			o.ExtraCores = append(o.ExtraCores, c)
		}
	}
}

// redactCore 是拦截敏感字段并将其值重写为 [REDACTED] 的 zapcore.Core 装饰器。
type redactCore struct {
	zapcore.Core
	keys map[string]struct{}
}

// newRedactCore 构造敏感字段拦截脱敏核心。
func newRedactCore(core zapcore.Core, keys []string) zapcore.Core {
	if len(keys) == 0 {
		keys = DefaultRedactKeys
	}
	keySet := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		keySet[strings.ToLower(strings.TrimSpace(k))] = struct{}{}
	}
	return &redactCore{
		Core: core,
		keys: keySet,
	}
}

func (c *redactCore) With(fields []zapcore.Field) zapcore.Core {
	return &redactCore{
		Core: c.Core.With(c.maskFields(fields)),
		keys: c.keys,
	}
}

func (c *redactCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *redactCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	return c.Core.Write(ent, c.maskFields(fields))
}

// maskFields 遍历字段列表，将敏感键的值替换为脱敏字符串。
func (c *redactCore) maskFields(fields []zapcore.Field) []zapcore.Field {
	if len(fields) == 0 {
		return fields
	}
	masked := make([]zapcore.Field, len(fields))
	for i, f := range fields {
		if _, exists := c.keys[strings.ToLower(f.Key)]; exists {
			masked[i] = zap.String(f.Key, "[REDACTED]")
		} else {
			masked[i] = f
		}
	}
	return masked
}

// ParseLevel 将字符串日志级别解析为 zapcore.Level。
func ParseLevel(lvl string) zapcore.Level {
	return parseZapLevel(lvl)
}

// parseZapLevel 将字符串日志级别解析为 zapcore.Level。
func parseZapLevel(lvl string) zapcore.Level {
	switch strings.ToLower(strings.TrimSpace(lvl)) {
	case "debug":
		return zapcore.DebugLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	case "dpanic":
		return zapcore.DPanicLevel
	case "panic":
		return zapcore.PanicLevel
	case "fatal":
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}

// NewWithOptions 遵循依赖倒置原则，依据函数式选项构建并发安全的 Zap 日志器。
func NewWithOptions(opts ...Option) *zap.Logger {
	opt := Options{
		Level:      "info",
		Channel:    "console",
		Redact:     true,
		RedactKeys: DefaultRedactKeys,
		AddCaller:  true,
	}
	for _, fn := range opts {
		fn(&opt)
	}

	level := parseZapLevel(opt.Level)

	ws := zapcore.AddSync(cmp.Or[io.Writer](opt.Writer, os.Stdout))

	encoder := buildEncoder(opt)
	core := zapcore.NewCore(encoder, ws, level)
	if opt.Redact {
		core = newRedactCore(core, opt.RedactKeys)
	}
	if len(opt.ExtraCores) > 0 {
		cores := append([]zapcore.Core{core}, opt.ExtraCores...)
		core = zapcore.NewTee(cores...)
	}

	zapOpts := make([]zap.Option, 0, 4)
	if opt.AddCaller {
		zapOpts = append(zapOpts, zap.AddCaller())
	}
	zapOpts = append(zapOpts, zap.AddStacktrace(zapcore.ErrorLevel))

	l := zap.New(core, zapOpts...)
	if opt.Channel == "json" && opt.ServiceName != "" {
		l = l.With(zap.String("service", opt.ServiceName))
	}

	setDefault(l)
	return l
}

// New 按配置构建日志器：保持既有签名完全兼容，内部转发至 NewWithOptions。
func New(cfg *config.Config, opts ...Option) *zap.Logger {
	allOpts := make([]Option, 0, 4+len(opts))
	allOpts = append(allOpts,
		WithLevel(cfg.Log.Level),
		WithChannel(cfg.Log.Channel),
		WithServiceName(cfg.App.Name),
		func(o *Options) { o.Env = cfg.App.Env },
	)
	allOpts = append(allOpts, opts...)
	return NewWithOptions(allOpts...)
}

// NewNop 返回一个无操作的空日志器（常用于单元测试）。
func NewNop() *zap.Logger {
	return zap.NewNop()
}

// SetDefault 显式设置全局默认的兜底日志器。
func SetDefault(l *zap.Logger) {
	if l != nil {
		setDefault(l)
	}
}

// buildEncoder 依据配置构建日志编码器。
func buildEncoder(opt Options) zapcore.Encoder {
	if opt.Channel == "json" {
		return buildJSONEncoder()
	}
	return buildConsoleEncoder(opt)
}

// buildJSONEncoder 构建面向机器采集的标准 JSON 编码器。
func buildJSONEncoder() zapcore.Encoder {
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.TimeKey = "timestamp"
	encCfg.EncodeTime = zapcore.TimeEncoderOfLayout(time.RFC3339)
	encCfg.EncodeDuration = zapcore.StringDurationEncoder
	encCfg.EncodeLevel = zapcore.LowercaseLevelEncoder
	return zapcore.NewJSONEncoder(encCfg)
}

// buildConsoleEncoder 构建面向本地开发的可读控制台编码器。
func buildConsoleEncoder(opt Options) zapcore.Encoder {
	encCfg := zap.NewDevelopmentEncoderConfig()
	encCfg.TimeKey = "timestamp"
	encCfg.EncodeTime = zapcore.TimeEncoderOfLayout("2006-01-02 15:04:05.000")
	encCfg.EncodeDuration = zapcore.StringDurationEncoder
	encCfg.ConsoleSeparator = "  "

	shouldColor := shouldColorFromEnv(opt.Env)
	if opt.NoColor != nil {
		shouldColor = !*opt.NoColor
	}

	encCfg.EncodeLevel = lo.Ternary(shouldColor, zapcore.CapitalColorLevelEncoder, zapcore.CapitalLevelEncoder)
	return newPrettyConsoleEncoder(encCfg, shouldColor)
}

// prettyConsoleEncoder 装饰原生 ConsoleEncoder，优化行尾上下文字段的可读性。
type prettyConsoleEncoder struct {
	zapcore.Encoder
	shouldColor bool
}

func newPrettyConsoleEncoder(encCfg zapcore.EncoderConfig, shouldColor bool) zapcore.Encoder {
	return &prettyConsoleEncoder{
		Encoder:     zapcore.NewConsoleEncoder(encCfg),
		shouldColor: shouldColor,
	}
}

func (e *prettyConsoleEncoder) Clone() zapcore.Encoder {
	return &prettyConsoleEncoder{
		Encoder:     e.Encoder.Clone(),
		shouldColor: e.shouldColor,
	}
}

func (e *prettyConsoleEncoder) EncodeEntry(ent zapcore.Entry, fields []zapcore.Field) (*buffer.Buffer, error) {
	line, err := e.Encoder.EncodeEntry(ent, fields)
	if err != nil {
		return nil, err
	}
	formatConsoleFields(line, e.shouldColor)
	return line, nil
}

// formatConsoleFields 将控制台日志末尾的 {"k":"v"} JSON 展开为平铺彩色的 key=value，提升人类肉眼阅读体验。
func formatConsoleFields(buf *buffer.Buffer, color bool) {
	bytes := buf.Bytes()
	start, end, ok := findTrailingJSONBlock(bytes)
	if !ok {
		return
	}

	var fields map[string]any
	if err := json.Unmarshal(bytes[start:end], &fields); err != nil || len(fields) == 0 {
		return
	}

	keys := slices.Sorted(maps.Keys(fields))

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(' ')
		}
		if color {
			sb.WriteString("\033[36m")
			sb.WriteString(k)
			sb.WriteString("\033[0m=")
		} else {
			sb.WriteString(k)
			sb.WriteByte('=')
		}
		writeFieldValue(&sb, fields[k])
	}

	prefix := bytes[:start]
	buf.Reset()
	buf.AppendBytes(prefix)
	buf.AppendString(sb.String())
	buf.AppendByte('\n')
}

// findTrailingJSONBlock 定位以换行符结尾的 buffer 末尾的 JSON 块。
func findTrailingJSONBlock(bytes []byte) (start, end int, ok bool) {
	n := len(bytes)
	if n < 3 || bytes[n-1] != '\n' || bytes[n-2] != '}' {
		return 0, 0, false
	}
	end = n - 1
	for i := end - 2; i >= 0; i-- {
		if bytes[i] == '{' && (i == 0 || bytes[i-1] == ' ' || bytes[i-1] == '\t') {
			return i, end, true
		}
	}
	return 0, 0, false
}

// writeFieldValue 将字段值格式化写入 builder，必要时加引号或直接输出。
func writeFieldValue(sb *strings.Builder, v any) {
	switch val := v.(type) {
	case string:
		if strings.ContainsAny(val, " \t\n\"") {
			sb.WriteString(strconv.Quote(val))
		} else {
			sb.WriteString(val)
		}
	default:
		fmt.Fprint(sb, val)
	}
}

// shouldColorFromEnv 根据环境标识与终端状态决定是否启用 ANSI 着色：
// 1. 遵循 https://no-color.org 规范：NO_COLOR 存在且非空时禁用着色；
// 2. 遵循 FORCE_COLOR 规范：值为 "0" 或 "false" 时显式禁用，其他非空值或 CLICOLOR_FORCE=1 时强制启用；
// 3. 本地开发环境（local/dev）且日志通道为 console 时默认启用着色；
// 4. 其他环境依标准输出是否为 TTY 字符终端决定。
func shouldColorFromEnv(env string) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if val := os.Getenv("FORCE_COLOR"); val != "" {
		return val != "0" && !strings.EqualFold(val, "false")
	}
	if os.Getenv("CLICOLOR_FORCE") == "1" {
		return true
	}
	if env == "local" || env == "development" {
		return true
	}
	return isTerminal()
}

// isTerminal 判断标准输出是否为交互式终端，非终端时关闭 ANSI 着色。
func isTerminal() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// WithContext 将日志器与请求标识注入 context，若存在活跃 OpenTelemetry Span 则自动同步注入 trace_id 与 span_id。
func WithContext(ctx context.Context, l *zap.Logger, requestID string) context.Context {
	l = cmp.Or(l, defaultLog())

	fields := make([]zap.Field, 0, 3)
	if requestID != "" {
		fields = append(fields, zap.String("request_id", requestID))
	}
	fields = append(fields, traceFields(ctx)...)

	return context.WithValue(ctx, ctxKey{}, l.With(fields...))
}

// FromContextOK 从 context 中提取请求作用域日志器。
func FromContextOK(ctx context.Context) (*zap.Logger, bool) {
	if ctx == nil {
		return nil, false
	}
	l, ok := ctx.Value(ctxKey{}).(*zap.Logger)
	return l, ok
}

// FromContext 提取请求作用域日志器，缺失时返回默认兜底日志器。
// 若上下文中未绑定请求日志器但存在活跃 Span，自动为兜底日志器附加 trace_id 与 span_id。
func FromContext(ctx context.Context) *zap.Logger {
	if l, ok := FromContextOK(ctx); ok {
		return l
	}
	l := defaultLog()
	if tf := traceFields(ctx); len(tf) > 0 {
		return l.With(tf...)
	}
	return l
}

// traceFields 尝试从上下文中解析活跃 OpenTelemetry Span 并提取 trace_id 与 span_id 结构化日志字段。
func traceFields(ctx context.Context) []zap.Field {
	if ctx == nil {
		return nil
	}
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.IsValid() {
		return nil
	}
	return []zap.Field{
		zap.String("trace_id", sc.TraceID().String()),
		zap.String("span_id", sc.SpanID().String()),
	}
}

// SugarFromContext 提取请求作用域的 SugaredLogger，方便需要松散键值对或格式化输出的场景。
func SugarFromContext(ctx context.Context) *zap.SugaredLogger {
	return FromContext(ctx).Sugar()
}

// Default 返回当前全局默认日志器。
func Default() *zap.Logger {
	return defaultLog()
}
