// Package logger 提供基于 zerolog 的高性能结构化日志器构建与请求作用域透传。
//
// 全系统统一以 *zerolog.Logger 传递日志器：nil 表示未注入，由 Nop() 兜底，
// 调用方因此无需逐处判空，且与 FromContext 的返回类型保持一致。
package logger

import (
	"bytes"
	"cmp"
	"context"
	"io"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace"

	"cgin/internal/config"
)

// requestIDKey 是请求标识在 context 中的私有键。
type requestIDKey struct{}

var (
	// nopLogger 是所有可选日志参数的零值占位，避免调用方逐处判空。
	nopLogger = zerolog.Nop()
	// defaultLogger 是装配完成前的全局兜底日志器，读多写少故用原子指针。
	defaultLogger atomic.Pointer[zerolog.Logger]
)

func init() {
	zerolog.TimestampFieldName = "timestamp"
	zerolog.LevelFieldName = "level"
	zerolog.MessageFieldName = "message"
	zerolog.TimeFieldFormat = time.RFC3339

	fallback := newFallbackLogger()
	defaultLogger.Store(&fallback)
}

// newFallbackLogger 创建系统启动早期或无配置时的兜底输出日志器。
func newFallbackLogger() zerolog.Logger {
	cw := zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "2006-01-02 15:04:05.000",
		NoColor:    true,
	}
	return zerolog.New(cw).
		Level(zerolog.ErrorLevel).
		With().
		Timestamp().
		Logger()
}

// defaultLog 返回当前兜底日志器，保证永不为 nil。
func defaultLog() *zerolog.Logger {
	if l := defaultLogger.Load(); l != nil {
		return l
	}
	return Nop()
}

// Nop 返回静默日志器指针，作为所有可选日志参数的零值兜底。
//
// 返回的是进程内共享实例，只可只读使用：zerolog 的 WithContext 会写回指针接收者，
// 严禁对返回值调用任何变异方法或直接解引用赋值。
func Nop() *zerolog.Logger {
	return &nopLogger
}

// Default 返回当前全局默认日志器。
func Default() *zerolog.Logger {
	return defaultLog()
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
		TimeFormat: "2006-01-02 15:04:05.000",
		PartsOrder: []string{
			zerolog.TimestampFieldName,
			zerolog.LevelFieldName,
			zerolog.CallerFieldName,
			zerolog.MessageFieldName,
		},
	}
}

// NewWithOptions 遵循依赖倒置原则，依据函数式选项构建并发安全的 Zerolog 日志器。
func NewWithOptions(opts ...Option) *zerolog.Logger {
	opt := Options{
		Level:     "info",
		Channel:   "console",
		Redact:    true,
		AddCaller: true,
	}
	for _, fn := range opts {
		fn(&opt)
	}

	var out io.Writer
	if opt.Channel == "json" {
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
	ctx := zerolog.New(out).Level(lvl).With().Timestamp()
	if opt.AddCaller {
		ctx = ctx.Caller()
	}
	if opt.Channel == "json" && opt.ServiceName != "" {
		ctx = ctx.Str("service", opt.ServiceName)
	}

	l := ctx.Logger()
	for _, h := range opt.Hooks {
		l = l.Hook(h)
	}

	defaultLogger.Store(&l)
	return &l
}

// New 按配置构建日志器，内部转发至 NewWithOptions。
func New(cfg *config.Config, opts ...Option) *zerolog.Logger {
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
// l 为 nil 时回退全局兜底日志器，调用方无需判空。
func WithContext(ctx context.Context, l *zerolog.Logger, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if l == nil {
		l = defaultLog()
	}
	c := l.With()
	if requestID != "" {
		c = c.Str("request_id", requestID)
		ctx = context.WithValue(ctx, requestIDKey{}, requestID)
	}
	if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
		c = c.Str("trace_id", sc.TraceID().String()).Str("span_id", sc.SpanID().String())
	}
	sub := c.Logger()
	return sub.WithContext(ctx)
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
func FromContextOK(ctx context.Context) (*zerolog.Logger, bool) {
	if ctx == nil {
		return nil, false
	}
	if l := zerolog.Ctx(ctx); l != nil && l.GetLevel() != zerolog.Disabled {
		return l, true
	}
	return nil, false
}

// FromContext 提取请求作用域日志器，缺失时返回默认兜底日志器指针（保证永不为 nil）。
// 若上下文中未绑定请求日志器但存在活跃 Span，自动为兜底日志器附加 trace_id 与 span_id。
func FromContext(ctx context.Context) *zerolog.Logger {
	if l, ok := FromContextOK(ctx); ok {
		return l
	}
	l := defaultLog()
	if ctx != nil {
		if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
			sub := l.With().
				Str("trace_id", sc.TraceID().String()).
				Str("span_id", sc.SpanID().String()).
				Logger()
			return &sub
		}
	}
	return l
}
