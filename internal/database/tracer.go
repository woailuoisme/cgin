package database

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"cgin/internal/config"
	"cgin/internal/platform/logger"
)

var (
	reSensitiveAssignments = regexp.MustCompile(`(?i)((?:["\x60]?[a-zA-Z0-9_]+["\x60]?\.)?["\x60]?(?:password|password_hash|token|secret|access_token|refresh_token|credential|api_key|verify_code|otp|value)["\x60]?\s*(?:=|ILIKE|LIKE)\s*)'(?:''|[^'])*'`)
	reArgon2               = regexp.MustCompile(`'?\$argon2[^\s'"]+'?`)
	reBcrypt               = regexp.MustCompile(`'?\$2[ayb]\$[0-9]{2}\$[^\s'"]{53}'?`)
	reJWT                  = regexp.MustCompile(`'?eyJh[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+'?`)
)

// queryTrace 承载单条 SQL 的起始时间与语句文本。
//
// pgx 的 TraceQueryEnd 不携带 SQL，因此必须在 TraceQueryStart 阶段把语句挂到 ctx 上。
type queryTrace struct {
	start time.Time
	sql   string
}

type ctxQueryTraceKey struct{}

// ZapTracer 将 pgx 的查询生命周期映射到系统的结构化日志体系。
// 支持全链路 request_id 透传、SQL 参数脱敏、调用栈溯源、慢查询自动捕获告警以及 Debug 级别的 SQL 审计。
type ZapTracer struct {
	slowThreshold time.Duration
	debugEnabled  bool
}

// NewZapTracer 创建一个基于 zap 的结构化查询追踪器。
func NewZapTracer(cfg config.DatabaseConfig) *ZapTracer {
	return &ZapTracer{
		slowThreshold: cfg.SlowThreshold,
		debugEnabled:  cfg.Debug,
	}
}

// TraceQueryStart 记录查询起始时间与语句。
func (t *ZapTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, ctxQueryTraceKey{}, queryTrace{start: time.Now(), sql: data.SQL})
}

// TraceQueryEnd 在 SQL 执行完成后触发，负责记录异常报错、慢查询告警与 Debug 审计。
func (t *ZapTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	trace, ok := ctx.Value(ctxQueryTraceKey{}).(queryTrace)
	if !ok {
		return
	}

	dur := time.Since(trace.start)
	log := logger.FromContext(ctx)

	query := RedactQuery(trace.sql)
	caller := captureCaller()
	operation := sqlOperation(trace.sql)

	fields := func(extra ...zap.Field) []zap.Field {
		f := make([]zap.Field, 0, 4+len(extra))
		f = append(f,
			zap.String("query", query),
			zap.String("duration", dur.String()),
			zap.String("operation", operation),
		)
		if caller != "" {
			f = append(f, zap.String("caller", caller))
		}
		return append(f, extra...)
	}

	// 1. 查询执行失败：排除 pgx.ErrNoRows（正常领域状态）与 context.Canceled（客户端断开连接）
	if isActionableDBError(data.Err) {
		log.Error("database query failed", fields(zap.Error(data.Err))...)
		return
	}

	// 2. 慢查询捕获：执行耗时超过阈值记录 Warn 日志
	if t.slowThreshold > 0 && dur >= t.slowThreshold {
		extra := []zap.Field{zap.String("threshold", t.slowThreshold.String())}
		if errors.Is(data.Err, context.Canceled) {
			extra = append(extra, zap.Bool("canceled", true))
		}
		log.Warn("slow database query detected", fields(extra...)...)
		return
	}

	// 3. 调试输出：当开启调试且当前日志级别为 Debug 或更低时以 Debug 级别输出
	if t.debugEnabled && log.Core().Enabled(zapcore.DebugLevel) {
		var extra []zap.Field
		if data.Err != nil {
			extra = append(extra, zap.Error(data.Err))
		}
		log.Debug("database query executed", fields(extra...)...)
	}
}

// isActionableDBError 判断数据库错误是否为需要记录与追踪的非预期故障。
func isActionableDBError(err error) bool {
	return err != nil && !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, context.Canceled)
}

// sqlOperation 提取 SQL 首个关键字作为操作类型，用于结构化检索。
func sqlOperation(query string) string {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return ""
	}
	for i, r := range trimmed {
		if r == ' ' || r == '\n' || r == '\t' {
			return strings.ToUpper(trimmed[:i])
		}
	}
	return strings.ToUpper(trimmed)
}

// RedactQuery 过滤并脱敏 SQL 中的敏感明文参数（口令、Hash、Token、验证码等）。
func RedactQuery(query string) string {
	if query == "" || !shouldRedact(query) {
		return query
	}

	result := redactInsertValues(query)
	result = reSensitiveAssignments.ReplaceAllString(result, "${1}'[REDACTED]'")
	result = reArgon2.ReplaceAllString(result, "'[REDACTED]'")
	result = reBcrypt.ReplaceAllString(result, "'[REDACTED]'")
	result = reJWT.ReplaceAllString(result, "'[REDACTED]'")
	return result
}

// shouldRedact 探测查询是否可能包含敏感字段或哈希特征，避免无谓的正则扫描。
func shouldRedact(query string) bool {
	lower := strings.ToLower(query)
	return strings.Contains(lower, "password") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "secret") ||
		strings.Contains(lower, "credential") ||
		strings.Contains(lower, "verify") ||
		strings.Contains(lower, "otp") ||
		strings.Contains(lower, "value") ||
		strings.Contains(query, "$argon2") ||
		strings.Contains(query, "$2a$") ||
		strings.Contains(query, "$2y$") ||
		strings.Contains(query, "$2b$") ||
		strings.Contains(query, "eyJh")
}

// redactInsertValues 识别 INSERT INTO 语句中针对敏感列的插入字面量并遮蔽。
func redactInsertValues(query string) string {
	upper := strings.ToUpper(strings.TrimSpace(query))
	if !strings.HasPrefix(upper, "INSERT INTO ") && !strings.HasPrefix(upper, "INSERT INTO\n") {
		return query
	}

	sensitiveIndices, valuesStart := extractInsertMeta(query)
	if len(sensitiveIndices) == 0 || valuesStart == -1 {
		return query
	}

	return maskInsertTuples(query, valuesStart, sensitiveIndices)
}

// extractInsertMeta 解析 INSERT INTO 语句中的敏感列下标映射及 VALUES 字面量的起始偏移。
func extractInsertMeta(query string) (map[int]bool, int) {
	openCol := strings.Index(query, "(")
	closeCol := strings.Index(query, ")")
	if openCol == -1 || closeCol == -1 || closeCol <= openCol {
		return nil, -1
	}

	colsStr := query[openCol+1 : closeCol]
	cols := strings.Split(colsStr, ",")
	sensitiveIndices := make(map[int]bool)
	for i, col := range cols {
		cleaned := strings.ToLower(strings.Trim(strings.TrimSpace(col), "`\"'"))
		switch cleaned {
		case "password", "password_hash", "token", "secret", "access_token",
			"refresh_token", "credential", "api_key", "verify_code", "otp", "value":
			sensitiveIndices[i] = true
		}
	}
	if len(sensitiveIndices) == 0 {
		return nil, -1
	}

	idxValues := strings.Index(strings.ToUpper(query[closeCol:]), "VALUES")
	if idxValues == -1 {
		return nil, -1
	}

	valuesStart := closeCol + idxValues + len("VALUES")
	return sensitiveIndices, valuesStart
}

// maskInsertTuples 扫描 VALUES 后的元组并将敏感列对应的字面量替换为 [REDACTED]。
func maskInsertTuples(query string, valuesStart int, sensitiveIndices map[int]bool) string {
	prefix := query[:valuesStart]
	valuesPart := query[valuesStart:]

	var result strings.Builder
	result.WriteString(prefix)

	inQuote := false
	tupleStart := -1
	lastEnd := 0

	for i := 0; i < len(valuesPart); i++ {
		c := valuesPart[i]
		switch {
		case c == '\'':
			if inQuote && i+1 < len(valuesPart) && valuesPart[i+1] == '\'' {
				i++
				continue
			}
			inQuote = !inQuote
		case inQuote:
			continue
		case c == '(' && tupleStart == -1:
			tupleStart = i
		case c == ')' && tupleStart != -1:
			result.WriteString(valuesPart[lastEnd : tupleStart+1])
			tupleContent := valuesPart[tupleStart+1 : i]
			result.WriteString(redactTupleValues(tupleContent, sensitiveIndices))
			result.WriteByte(')')
			lastEnd = i + 1
			tupleStart = -1
		}
	}

	if lastEnd < len(valuesPart) {
		result.WriteString(valuesPart[lastEnd:])
	}

	return result.String()
}

// redactTupleValues 处理单个 INSERT 元组内的字段列表并遮蔽敏感列值。
func redactTupleValues(tuple string, sensitiveIndices map[int]bool) string {
	parts := splitSQLValues(tuple)
	for i, part := range parts {
		if !sensitiveIndices[i] {
			continue
		}
		trimmed := strings.TrimSpace(part)
		upperVal := strings.ToUpper(trimmed)
		if upperVal == "DEFAULT" || upperVal == "NULL" {
			continue
		}
		leadingSpaces := part[:len(part)-len(strings.TrimLeft(part, " \t\r\n"))]
		parts[i] = leadingSpaces + "'[REDACTED]'"
	}
	return strings.Join(parts, ",")
}

// splitSQLValues 切分单条 SQL 元组中的逗号分隔值，正确处理带单引号的字符串字面量。
func splitSQLValues(s string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			if inQuote && i+1 < len(s) && s[i+1] == '\'' {
				current.WriteByte(c)
				current.WriteByte(s[i+1])
				i++
				continue
			}
			inQuote = !inQuote
			current.WriteByte(c)
		case c == ',' && !inQuote:
			parts = append(parts, current.String())
			current.Reset()
		default:
			current.WriteByte(c)
		}
	}
	parts = append(parts, current.String())
	return parts
}

// captureCaller 自动提取触发数据库调用的首个业务层代码文件名与行号。
func captureCaller() string {
	const depth = 20
	var pcs [depth]uintptr
	n := runtime.Callers(3, pcs[:])
	if n == 0 {
		return ""
	}

	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		file := frame.File
		if isInternalStack(file) {
			if !more {
				break
			}
			continue
		}
		return formatCaller(frame.File, frame.Line)
	}
	return ""
}

// isInternalStack 判断指定栈帧是否属于应被忽略的基础设施底层（标准库、pgx 驱动或 database 核心引擎）。
func isInternalStack(file string) bool {
	f := filepath.ToSlash(file)
	if strings.Contains(f, "runtime/") ||
		strings.Contains(f, "database/sql") ||
		strings.Contains(f, "github.com/jackc/pgx") ||
		strings.Contains(f, "github.com/exaring/otelpgx") {
		return true
	}
	return isInternalDatabaseFrame(file)
}

// isInternalDatabaseFrame 判定是否属于 internal/database/ 核心引擎自身（排除 seeders/ 与测试）。
func isInternalDatabaseFrame(file string) bool {
	f := filepath.ToSlash(file)
	// 测试文件是调用方而非引擎，不能连调用方一起跳过。
	if strings.HasSuffix(f, "_test.go") {
		return false
	}
	_, rest, found := strings.Cut(f, "internal/database/")
	if !found {
		return false
	}
	if strings.HasPrefix(rest, "seeders/") {
		return false
	}
	return true
}

// formatCaller 将完整文件路径格式化为便于结构化检索的精简定位表示。
func formatCaller(file string, line int) string {
	f := filepath.ToSlash(file)
	for _, root := range []string{"internal/", "tests/", "cmd/"} {
		if _, rest, found := strings.Cut(f, root); found {
			return fmt.Sprintf("%s%s:%d", root, rest, line)
		}
	}
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}
