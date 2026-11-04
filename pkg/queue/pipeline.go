package queue

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"time"

	"cgin/pkg/errs"
	"cgin/pkg/logger"
)

// PipelineHandler 描述任务在中间件管线中的统一执行函数契约。
type PipelineHandler func(ctx context.Context, taskName string, payload []byte) error

// Middleware 采用标准洋葱模型包装任务执行逻辑。
type Middleware func(next PipelineHandler) PipelineHandler

// Chain 将多个中间件按声明顺序编译组合为一个复合 PipelineHandler。
// 先声明的中间件在最外层（先进入，后离开）。
func Chain(final PipelineHandler, middlewares ...Middleware) PipelineHandler {
	h := final
	for _, m := range slices.Backward(middlewares) {
		if m != nil {
			h = m(h)
		}
	}
	return h
}

// RecoveryMiddleware 提供恐慌恢复兜底保护，捕获 panic 堆栈并转为错误返回。
func RecoveryMiddleware(l *slog.Logger) Middleware {
	return func(next PipelineHandler) PipelineHandler {
		return func(ctx context.Context, taskName string, payload []byte) (execErr error) {
			defer func() {
				if r := recover(); r != nil {
					stack := string(debug.Stack())
					cmp.Or(l, logger.Nop()).ErrorContext(ctx, "panic recovered in queue task execution",
						slog.String("task", taskName),
						slog.Any("panic", r),
						slog.String("stack", stack),
					)
					var cause error
					if err, ok := r.(error); ok {
						cause = err
					} else {
						cause = fmt.Errorf("%v", r)
					}
					execErr = fmt.Errorf("queue: task %q panicked: %w", taskName, cause)
				}
			}()
			return next(ctx, taskName, payload)
		}
	}
}

// TracingMiddleware 将 TaskEnvelope 中透传的追踪元数据（如 RequestID）绑定到 ctx 与 logger。
func TracingMiddleware(l *slog.Logger, headers map[string]string) Middleware {
	return func(next PipelineHandler) PipelineHandler {
		return func(ctx context.Context, taskName string, payload []byte) error {
			if reqID := headers["request_id"]; reqID != "" {
				ctx = logger.WithContext(ctx, l, reqID)
			}
			return next(ctx, taskName, payload)
		}
	}
}

// LockingMiddleware 提供基于分布式锁的防并发重叠防护。
func LockingMiddleware(locker Locker, router *TaskRouter, l *slog.Logger) Middleware {
	return func(next PipelineHandler) PipelineHandler {
		return func(ctx context.Context, taskName string, payload []byte) error {
			if locker == nil || router == nil {
				return next(ctx, taskName, payload)
			}

			ttl, enabled := router.GetLockTTL(taskName)
			if !enabled {
				return next(ctx, taskName, payload)
			}

			lockKey := "schedule:lock:" + taskName
			acquired, err := TryLock(ctx, locker, lockKey, ttl, func(lockCtx context.Context) error {
				return next(lockCtx, taskName, payload)
			})
			if err != nil {
				return err
			}
			if !acquired {
				cmp.Or(l, logger.Nop()).WarnContext(ctx, "task skipped due to active overlap lock",
					slog.String("task", taskName),
					slog.String("lock_key", lockKey),
				)
				return nil
			}

			return nil
		}
	}
}

// LoggingMiddleware 统计任务处理耗时并记录结构化执行日志。
func LoggingMiddleware(l *slog.Logger) Middleware {
	return func(next PipelineHandler) PipelineHandler {
		return func(ctx context.Context, taskName string, payload []byte) error {
			log := cmp.Or(l, logger.Nop())
			start := time.Now()
			err := next(ctx, taskName, payload)
			duration := time.Since(start)

			if err != nil {
				attrs := []slog.Attr{
					slog.String("task", taskName),
					slog.Duration("duration", duration),
					slog.Any("error", err),
				}
				for _, f := range errs.OopsFields(err) {
					attrs = append(attrs, slog.Any(f.Key, f.Value))
				}
				log.LogAttrs(ctx, slog.LevelError, "task execution failed", attrs...)
				return err
			}

			log.DebugContext(ctx, "task execution succeeded",
				slog.String("task", taskName),
				slog.Duration("duration", duration),
			)
			return nil
		}
	}
}
