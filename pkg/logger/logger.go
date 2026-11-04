// Package logger 提供基于 Go 官方标准库 log/slog 与 samber/slog-zerolog Handler 的结构化日志器构建与请求作用域透传。
//
// 全系统统一以 *slog.Logger 传递日志器：nil 表示未注入，由 Nop() 兜底，
// 调用方因此无需逐处判空，且与 FromContext 的返回类型保持一致。
package logger

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	slogcommon "github.com/samber/slog-common"
	slogzerolog "github.com/samber/slog-zerolog/v2"
	"go.opentelemetry.io/otel/trace"

	"cgin/internal/config"
)

// ChannelJSON 与 ChannelConsole 描述输出通道格式。
const (
	ChannelJSON    = "json"
	ChannelConsole = "console"
)

// Logger 是标准库 *slog.Logger 的类型别名，保证系统对外统一认知。
type Logger = slog.Logger

// requestIDKey 是请求标识在 context 中的私有键。
type requestIDKey struct{}

// contextLoggerKey 是请求作用域日志器在 context 中的私有键。
type contextLoggerKey struct{}

func formatCallerPC(pc uintptr) string {
	fs := runtime.CallersFrames([]uintptr{pc})
	frame, _ := fs.Next()
	if frame.File == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", filepath.Base(frame.File), frame.Line)
}

// TraceContextHandler 拦截 slog 调用，从 context 中自动提取链路标识（TraceID/SpanID/RequestID）并注入 Record 属性。
type TraceContextHandler struct {
	next      slog.Handler
	zlog      *zerolog.Logger
	channel   string
	addCaller bool
}

var _ slog.Handler = (*TraceContextHandler)(nil)

// NewTraceContextHandler 包装目标 Handler，实现上下文元数据与智能 Caller 格式化的自动透传。
func NewTraceContextHandler(next slog.Handler, zlog *zerolog.Logger, channel string, addCaller bool) *TraceContextHandler {
	return &TraceContextHandler{
		next:      next,
		zlog:      zlog,
		channel:   channel,
		addCaller: addCaller,
	}
}

func (h *TraceContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *TraceContextHandler) attachTraceAttrs(ctx context.Context, r *slog.Record) {
	if ctx == nil {
		return
	}

	// 1. 自动提取并注入 OpenTelemetry TraceID 与 SpanID（仅在 JSON 模式下全量透传给 APM）
	if h.channel == ChannelJSON {
		span := trace.SpanFromContext(ctx)
		if span != nil && span.SpanContext().IsValid() {
			sc := span.SpanContext()
			r.AddAttrs(
				slog.String("trace_id", sc.TraceID().String()),
				slog.String("span_id", sc.SpanID().String()),
			)
		}
	}

	// 2. 自动提取并注入 HTTP/Worker 链路请求标识 RequestID
	// - JSON 模式：全量提供给 APM/Loki 搜集；
	// - 控制台模式：仅在 Warn/Error 时提供排查关联，普通 Info/Debug 极简过滤。
	if reqID := RequestIDFromContext(ctx); reqID != "" {
		if h.channel == ChannelJSON || r.Level >= slog.LevelWarn {
			r.AddAttrs(slog.String("request_id", reqID))
		}
	}
}

func (h *TraceContextHandler) attachCallerAttr(r *slog.Record) {
	// 3. 智能解析并注入相对路径代码位置（Caller）
	// - 控制台模式：仅在 Error 级别时展示，格式为 file.go:line >，对齐行业极简风格，避免日常 Info/Debug/Warn 视觉噪音；
	// - JSON 生产模式：若启用 AddCaller，则对所有级别提供规范的 caller 属性。
	shouldAddCaller := (h.channel == ChannelJSON && h.addCaller) || (h.channel == ChannelConsole && r.Level >= slog.LevelError)
	if shouldAddCaller && r.PC != 0 {
		if c := formatCallerPC(r.PC); c != "" {
			r.AddAttrs(slog.String(zerolog.CallerFieldName, c))
		}
	}
}

func (h *TraceContextHandler) Handle(ctx context.Context, r slog.Record) error {
	h.attachTraceAttrs(ctx, &r)
	h.attachCallerAttr(&r)
	return h.next.Handle(ctx, r)
}

func (h *TraceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceContextHandler{
		next:      h.next.WithAttrs(attrs),
		zlog:      h.zlog,
		channel:   h.channel,
		addCaller: h.addCaller,
	}
}

func (h *TraceContextHandler) WithGroup(name string) slog.Handler {
	return &TraceContextHandler{
		next:      h.next.WithGroup(name),
		zlog:      h.zlog,
		channel:   h.channel,
		addCaller: h.addCaller,
	}
}

var (
	// nopLogger 是所有可选日志参数的零值占位（基于官方 DiscardHandler，零内存分配）。
	nopLogger = slog.New(slog.DiscardHandler)
	// defaultLogger 是装配完成前的全局兜底日志器，读多写少故用原子指针。
	defaultLogger atomic.Pointer[slog.Logger]
	// defaultZerolog 保存全局兜底的底层 zerolog 实例。
	defaultZerolog atomic.Pointer[zerolog.Logger]
)

func buildSlogConverter() slogzerolog.Converter {
	return func(addSource bool, replaceAttr func(groups []string, a slog.Attr) slog.Attr, loggerAttr []slog.Attr, groups []string, record *slog.Record) map[string]any {
		attrs := slogcommon.AppendRecordAttrsToAttrs(loggerAttr, groups, record)
		for i := range attrs {
			if attrs[i].Value.Kind() == slog.KindDuration {
				attrs[i] = slog.String(attrs[i].Key, attrs[i].Value.Duration().String())
			}
		}
		attrs = slogcommon.ReplaceAttrs(replaceAttr, []string{}, attrs...)
		attrs = slogcommon.RemoveEmptyAttrs(attrs)
		return slogcommon.AttrsToMap(attrs...)
	}
}

func init() {
	zerolog.TimestampFieldName = "timestamp"
	zerolog.LevelFieldName = "level"
	zerolog.MessageFieldName = "message"
	zerolog.TimeFieldFormat = time.RFC3339

	fallbackZ := newFallbackLogger()
	defaultZerolog.Store(&fallbackZ)

	fallbackH := slogzerolog.Option{
		Level:     slog.LevelError,
		Logger:    &fallbackZ,
		Converter: buildSlogConverter(),
	}.NewZerologHandler()

	fallbackS := slog.New(NewTraceContextHandler(fallbackH, &fallbackZ, "console", false))
	defaultLogger.Store(fallbackS)
}

// newFallbackLogger 创建系统启动早期或无配置时的兜底输出日志器。
func newFallbackLogger() zerolog.Logger {
	cw := zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
		NoColor:    true,
	}
	return zerolog.New(cw).
		Level(zerolog.ErrorLevel).
		With().
		Timestamp().
		Logger()
}

// defaultLog 返回当前兜底日志器，保证永不为 nil。
func defaultLog() *slog.Logger {
	if l := defaultLogger.Load(); l != nil {
		return l
	}
	return nopLogger
}

// Nop 返回官方无操作静默日志器指针，作为所有可选日志参数的零值兜底。
func Nop() *slog.Logger {
	return nopLogger
}

// Default 返回当前全局默认日志器。
func Default() *slog.Logger {
	return defaultLog()
}

// ToZerolog 从 *slog.Logger 中尝试提取底层的 *zerolog.Logger 实例；若未找到则回退默认全局 zerolog。
func ToZerolog(l *slog.Logger) *zerolog.Logger {
	if l == nil {
		return defaultZerolog.Load()
	}
	if th, ok := l.Handler().(*TraceContextHandler); ok && th.zlog != nil {
		return th.zlog
	}
	if dz := defaultZerolog.Load(); dz != nil {
		return dz
	}
	z := zerolog.Nop()
	return &z
}

// FromZerolog 将原生 *zerolog.Logger 包装为带 TraceContext 拦截的 *slog.Logger。
func FromZerolog(z *zerolog.Logger) *slog.Logger {
	if z == nil {
		return Nop()
	}
	h := slogzerolog.Option{
		Level:  slogLevelFromZerolog(z.GetLevel()),
		Logger: z,
	}.NewZerologHandler()
	return slog.New(NewTraceContextHandler(h, z, "console", false))
}

// slogLevelFromZerolog 将 zerolog.Level 映射为 slog.Level。
func slogLevelFromZerolog(lvl zerolog.Level) slog.Level {
	switch lvl {
	case zerolog.DebugLevel, zerolog.TraceLevel:
		return slog.LevelDebug
	case zerolog.WarnLevel:
		return slog.LevelWarn
	case zerolog.ErrorLevel, zerolog.FatalLevel, zerolog.PanicLevel:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
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
	AddCaller   bool
	Hooks       []zerolog.Hook
}

// Option 定义日志器构建的函数式选项。
type Option func(*Options)

// WithLevel 设置最低日志级别（"debug", "info", "warn", "error", "panic", "fatal"）。
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

// WithRedaction 设置是否启用敏感数据脱敏过滤器（键名取自 DefaultRedactKeys）。
func WithRedaction(enabled bool) Option {
	return func(o *Options) {
		o.Redact = enabled
	}
}

// WithCaller 设置是否开启调用方位置（文件名与行号）记录。
func WithCaller(enabled bool) Option {
	return func(o *Options) {
		o.AddCaller = enabled
	}
}

// WithHook 附加自定义 zerolog.Hook 输出钩子（例如 OpenTelemetry 遥测事件桥接）。
func WithHook(hook zerolog.Hook) Option {
	return func(o *Options) {
		if hook != nil {
			o.Hooks = append(o.Hooks, hook)
		}
	}
}

// ParseLevel 将字符串日志级别解析为 zerolog.Level。
func ParseLevel(lvl string) zerolog.Level {
	switch strings.ToLower(strings.TrimSpace(lvl)) {
	case "debug":
		return zerolog.DebugLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "panic":
		return zerolog.PanicLevel
	case "fatal":
		return zerolog.FatalLevel
	default:
		return zerolog.InfoLevel
	}
}

// ParseSlogLevel 将字符串日志级别解析为 slog.Level。
func ParseSlogLevel(lvl string) slog.Level {
	return slogLevelFromZerolog(ParseLevel(lvl))
}

// jsonRedactedValue 是 JSON 字段脱敏后的占位值。
const jsonRedactedValue = `"[REDACTED]"`

// jsonRedactingWriter 专职处理 JSON 格式的敏感数据脱敏，执行零正则、字符串感知的结构扫描。
type jsonRedactingWriter struct {
	out  io.Writer
	keys map[string]struct{}
}

func newJSONRedactingWriter(out io.Writer) io.Writer {
	set := make(map[string]struct{}, len(DefaultRedactKeys))
	for _, k := range DefaultRedactKeys {
		trimmed := strings.TrimSpace(k)
		if trimmed != "" {
			set[strings.ToLower(trimmed)] = struct{}{}
		}
	}
	return &jsonRedactingWriter{out: out, keys: set}
}

func (w *jsonRedactingWriter) Write(p []byte) (n int, err error) {
	replaced := w.redactJSON(p)
	written, err := w.out.Write(replaced)
	if err == nil && written != len(replaced) {
		return len(p), io.ErrShortWrite
	}
	return len(p), err
}

// consoleRedactingWriter 专职处理控制台 key=value 格式的敏感数据脱敏。
type consoleRedactingWriter struct {
	out io.Writer
	kv  *regexp.Regexp
}

func newConsoleRedactingWriter(out io.Writer) io.Writer {
	escaped := make([]string, 0, len(DefaultRedactKeys))
	for _, k := range DefaultRedactKeys {
		trimmed := strings.TrimSpace(k)
		if trimmed != "" {
			escaped = append(escaped, regexp.QuoteMeta(trimmed))
		}
	}

	kv := regexp.MustCompile(`(?i)(\b(?:` + strings.Join(escaped, "|") + `)=)(?:"[^"]*"|[^\s\n,}"]+)`)
	return &consoleRedactingWriter{out: out, kv: kv}
}

func (w *consoleRedactingWriter) Write(p []byte) (n int, err error) {
	replaced := w.kv.ReplaceAllFunc(p, redactConsolePair)
	written, err := w.out.Write(replaced)
	if err == nil && written != len(replaced) {
		return len(p), io.ErrShortWrite
	}
	return len(p), err
}

// redactConsolePair 把控制台的 key=value 片段替换为 key=[REDACTED]。
func redactConsolePair(match []byte) []byte {
	s := string(match)
	idx := strings.Index(s, "=")
	if idx == -1 {
		return match
	}
	return []byte(s[:idx+1] + "[REDACTED]")
}

// redactJSON 扫描 JSON 文本，把敏感键的取值整体替换为占位符。
//
// 支持字符串、数字、布尔、null 以及嵌套的对象与数组；字符串内部的引号与转义已正确处理。
func (w *jsonRedactingWriter) redactJSON(p []byte) []byte {
	// 控制台通道的普通行不含引号，不必为整行重新分配缓冲。
	if bytes.IndexByte(p, '"') < 0 {
		return p
	}

	out := make([]byte, 0, len(p))

	for i := 0; i < len(p); {
		if p[i] != '"' {
			out = append(out, p[i])
			i++
			continue
		}

		strEnd, ok := scanJSONString(p, i)
		if !ok {
			out = append(out, p[i:]...)
			break
		}

		valueStart, valueEnd, matched := w.sensitiveFieldSpan(p, i, strEnd)
		if !matched {
			out = append(out, p[i:strEnd]...)
			i = strEnd
			continue
		}

		out = append(out, p[i:valueStart]...)
		out = append(out, jsonRedactedValue...)
		i = valueEnd
	}

	return out
}

// sensitiveFieldSpan 判断 "key" 之后是否紧跟冒号且键命中敏感集合，命中则返回取值的起止下标。
func (w *jsonRedactingWriter) sensitiveFieldSpan(p []byte, keyStart, keyEnd int) (int, int, bool) {
	if _, sensitive := w.keys[strings.ToLower(string(p[keyStart+1:keyEnd-1]))]; !sensitive {
		return 0, 0, false
	}

	colon := skipJSONSpace(p, keyEnd)
	if colon >= len(p) || p[colon] != ':' {
		return 0, 0, false
	}

	valueStart := skipJSONSpace(p, colon+1)
	valueEnd, ok := scanJSONValue(p, valueStart)
	if !ok {
		return 0, 0, false
	}
	return valueStart, valueEnd, true
}

// skipJSONSpace 跳过 ASCII 空白字符。
func skipJSONSpace(p []byte, i int) int {
	for i < len(p) && (p[i] == ' ' || p[i] == '\t') {
		i++
	}
	return i
}

// scanJSONString 返回字符串字面量结束后的下标，i 必须指向起始双引号。
func scanJSONString(p []byte, i int) (int, bool) {
	if i >= len(p) || p[i] != '"' {
		return 0, false
	}
	for j := i + 1; j < len(p); j++ {
		switch p[j] {
		case '\\':
			j++ // 跳过被转义的字符
		case '"':
			return j + 1, true
		}
	}
	return 0, false
}

// scanJSONValue 返回任意 JSON 取值的结束下标（不含），覆盖标量与复合结构。
func scanJSONValue(p []byte, i int) (int, bool) {
	if i >= len(p) {
		return 0, false
	}

	switch p[i] {
	case '"':
		return scanJSONString(p, i)
	case '{', '[':
		return scanJSONComposite(p, i)
	default:
		j := i
		for j < len(p) {
			switch p[j] {
			case ',', '}', ']', ' ', '\t', '\n':
				if j == i {
					return 0, false
				}
				return j, true
			}
			j++
		}
		return j, j > i
	}
}

// scanJSONComposite 用括号配对定位对象/数组结束位置，字符串内容不参与配对。
func scanJSONComposite(p []byte, i int) (int, bool) {
	var closing byte
	switch p[i] {
	case '{':
		closing = '}'
	case '[':
		closing = ']'
	default:
		return 0, false
	}

	depth := 0
	for j := i; j < len(p); j++ {
		switch p[j] {
		case '"':
			end, ok := scanJSONString(p, j)
			if !ok {
				return 0, false
			}
			j = end - 1
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				if p[j] != closing {
					return 0, false
				}
				return j + 1, true
			}
		}
	}
	// 畸形 JSON 宁可漏脱敏也不误伤正常字段：唯一调用方是 zerolog，输出恒为合法 JSON。
	return 0, false
}

// buildConsoleWriter 构建面向本地控制台的着色/格式化写入器。
func buildConsoleWriter(opt Options) zerolog.ConsoleWriter {
	out := cmp.Or[io.Writer](opt.Writer, os.Stdout)
	shouldColor := shouldColorFromEnv(opt.Env)
	if opt.NoColor != nil {
		shouldColor = !*opt.NoColor
	}

	return zerolog.ConsoleWriter{
		Out:        out,
		NoColor:    !shouldColor,
		TimeFormat: "15:04:05",
		PartsOrder: []string{
			zerolog.TimestampFieldName,
			zerolog.LevelFieldName,
			zerolog.CallerFieldName,
			zerolog.MessageFieldName,
		},
		FieldsOrder: []string{
			"method",
			"path",
			"status",
			"latency",
			"ip",
		},
		FieldsExclude: []string{
			"source",
			"trace_id",
			"span_id",
		},
		FormatCaller: func(i any) string {
			var c string
			if cc, ok := i.(string); ok {
				c = cc
			}
			if c == "" {
				return ""
			}
			if !shouldColor {
				return c + " >"
			}
			return "\x1b[36m" + c + "\x1b[0m \x1b[33m>\x1b[0m"
		},
	}
}

// buildZerologCore 构建底层的 zerolog.Logger 实例。
func buildZerologCore(opt Options) *zerolog.Logger {
	var out io.Writer
	if opt.Channel == ChannelJSON {
		target := cmp.Or[io.Writer](opt.Writer, os.Stdout)
		if opt.Redact {
			target = newJSONRedactingWriter(target)
		}
		out = target
	} else {
		cw := buildConsoleWriter(opt)
		if opt.Redact {
			cw.Out = newConsoleRedactingWriter(cw.Out)
		}
		out = cw
	}

	lvl := ParseLevel(opt.Level)
	builder := zerolog.New(out).Level(lvl).With().Timestamp()
	if opt.Channel == ChannelJSON && opt.ServiceName != "" {
		builder = builder.Str("service", opt.ServiceName)
	}

	l := builder.Logger()
	for _, h := range opt.Hooks {
		l = l.Hook(h)
	}
	return &l
}

// NewWithOptions 遵循依赖倒置原则，依据函数式选项构建并发安全的日志器。
func NewWithOptions(opts ...Option) *slog.Logger {
	opt := Options{
		Level:     "info",
		Channel:   "console",
		Redact:    true,
		AddCaller: true,
	}
	for _, fn := range opts {
		fn(&opt)
	}

	zlog := buildZerologCore(opt)
	defaultZerolog.Store(zlog)

	baseHandler := slogzerolog.Option{
		Level:     slogLevelFromZerolog(zlog.GetLevel()),
		Logger:    zlog,
		AddSource: false, // 彻底关闭 slogzerolog 默认的 source={...} JSON 字段倾倒，统一由 TraceContextHandler 精准注入
		Converter: buildSlogConverter(),
	}.NewZerologHandler()

	pipelineHandler := NewTraceContextHandler(baseHandler, zlog, opt.Channel, opt.AddCaller)
	sl := slog.New(pipelineHandler)

	defaultLogger.Store(sl)
	return sl
}

// New 按配置构建日志器，内部转发至 NewWithOptions。
func New(cfg config.LogConfig, opts ...Option) *slog.Logger {
	allOpts := make([]Option, 0, 4+len(opts))
	allOpts = append(allOpts,
		WithLevel(cfg.Level),
		WithChannel(cfg.Channel),
		WithServiceName(cfg.ServiceName),
		func(o *Options) { o.Env = cfg.Env },
	)
	allOpts = append(allOpts, opts...)
	return NewWithOptions(allOpts...)
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

// WithContext 将日志器与请求标识注入 context。
// l 为 nil 时回退全局兜底日志器，调用方无需判空。
func WithContext(ctx context.Context, l *slog.Logger, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if l == nil {
		l = defaultLog()
	}
	if requestID != "" {
		ctx = context.WithValue(ctx, requestIDKey{}, requestID)
	}
	return context.WithValue(ctx, contextLoggerKey{}, l)
}

// RequestIDFromContext 从 context 中安全提取请求标识 request_id。
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// FromContextOK 从 context 中提取请求作用域日志器。
func FromContextOK(ctx context.Context) (*slog.Logger, bool) {
	if ctx == nil {
		return nil, false
	}
	if l, ok := ctx.Value(contextLoggerKey{}).(*slog.Logger); ok && l != nil {
		return l, true
	}
	return nil, false
}

// FromContext 提取请求作用域日志器，缺失时返回默认兜底日志器指针（保证永不为 nil）。
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := FromContextOK(ctx); ok {
		return l
	}
	return defaultLog()
}
