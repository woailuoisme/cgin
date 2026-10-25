package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"cgin/internal/config"
	"cgin/internal/platform/logger"
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

var _ pgx.QueryTracer = (*ZapTracer)(nil)

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
//
// 日志只携带语句指纹与操作类型：pgx 扩展协议下参数位于 Args，语句文本只有 $n 占位符，
// 因此无需手写 SQL 词法器去猜字面量边界，也就不存在脱敏遗漏导致的密钥泄漏。
func (t *ZapTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	trace, ok := ctx.Value(ctxQueryTraceKey{}).(queryTrace)
	if !ok {
		return
	}

	dur := time.Since(trace.start)
	log := logger.FromContext(ctx)

	failed := isActionableDBError(data.Err)
	slow := t.slowThreshold > 0 && dur >= t.slowThreshold
	debug := t.debugEnabled && log.Core().Enabled(zapcore.DebugLevel)
	if !failed && !slow && !debug {
		return
	}

	fields := func(extra ...zap.Field) []zap.Field {
		f := make([]zap.Field, 0, 5+len(extra))
		f = append(f,
			zap.String("operation", sqlOperation(trace.sql)),
			zap.String("fingerprint", QueryFingerprint(trace.sql)),
			zap.String("duration", dur.String()),
		)
		// 仅在实际写日志时采集调用栈，避免每条查询都付一次 runtime.Callers。
		if caller := captureCaller(); caller != "" {
			f = append(f, zap.String("caller", caller))
		}
		return append(f, extra...)
	}

	// 1. 查询执行失败：排除 pgx.ErrNoRows（正常领域状态）与 context.Canceled（客户端断开连接）
	if failed {
		log.Error("database query failed", fields(zap.Error(data.Err))...)
		return
	}

	// 2. 慢查询捕获：执行耗时超过阈值记录 Warn 日志
	if slow {
		extra := []zap.Field{zap.String("threshold", t.slowThreshold.String())}
		if errors.Is(data.Err, context.Canceled) {
			extra = append(extra, zap.Bool("canceled", true))
		}
		log.Warn("slow database query detected", fields(extra...)...)
		return
	}

	// 3. 调试输出：仅在语句不含字面量时附带原文，否则只留指纹。
	var extra []zap.Field
	if statement, safe := loggableStatement(trace.sql); safe {
		extra = append(extra, zap.String("statement", statement))
	}
	if data.Err != nil {
		extra = append(extra, zap.Error(data.Err))
	}
	log.Debug("database query executed", fields(extra...)...)
}

// QueryFingerprint 返回语句的稳定短指纹：折叠连续空白后取 SHA-256 前 8 字节。
//
// 指纹只用于聚合与比对：同一语句的取模参数变化不会改变指纹，
// 日志侧因此可以在不记录任何参数的前提下把慢查询归堆。
func QueryFingerprint(query string) string {
	normalised := strings.Join(strings.Fields(query), " ")
	sum := sha256.Sum256([]byte(normalised))
	return hex.EncodeToString(sum[:8])
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

// loggableStatement 返回可安全写入日志的语句原文。
//
// pgx 扩展协议下参数位于 Args，语句本身只有 $n 占位符；一旦语句内出现单引号，
// 说明调用方拼接了字面量（或使用了 simple protocol），此时只保留指纹、不输出原文。
func loggableStatement(query string) (string, bool) {
	if strings.ContainsRune(query, '\'') {
		return "", false
	}
	return strings.Join(strings.Fields(query), " "), true
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
