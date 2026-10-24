package queue

import (
	"context"
	"fmt"
	"runtime/debug"
	"slices"
	"time"

	"go.uber.org/zap"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/logger"
	platformsentry "bgin/internal/platform/sentry"
)

// Handler 描述任务执行的原子处理函数契约 (SRP)。
type Handler func(ctx context.Context, job Job) error

// Middleware 采用标准洋葱模型包装任务执行逻辑 (OCP)。
type Middleware func(next Handler) Handler

// Chain 将多个中间件按顺序编译组合为一个复合 Handler。
// 先声明的中间件在外层（先进入，后离开）。
func Chain(h Handler, middlewares ...Middleware) Handler {
	for _, m := range slices.Backward(middlewares) {
		if m != nil {
			h = m(h)
		}
	}
	return h
}

func resolveLogger(l *zap.Logger) *zap.Logger {
	if l == nil {
		return fallbackLog()
	}
	return l
}

// RecoveryMiddleware 提供恐慌恢复兜底保护，捕获 panic 堆栈并转为错误返回。
func RecoveryMiddleware(l *zap.Logger) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, job Job) (execErr error) {
			defer func() {
				if r := recover(); r != nil {
					stack := string(debug.Stack())
					resolveLogger(l).Error("panic recovered in task execution",
						zap.String("task", job.TaskName()),
						zap.Any("panic", r),
						zap.String("stack", stack),
					)
					execErr = fmt.Errorf("queue: job panic: %v", r)
					platformsentry.CaptureExceptionWithContext(ctx, execErr)
				}
			}()
			return next(ctx, job)
		}
	}
}

// TracingMiddleware 将 TaskEnvelope 中透传的追踪元数据（如 RequestID）绑定到 ctx 与 logger。
func TracingMiddleware(l *zap.Logger, headers map[string]string) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, job Job) error {
			if reqID := headers["request_id"]; reqID != "" {
				ctx = logger.WithContext(ctx, l, reqID)
				platformsentry.SetTag(ctx, "request_id", reqID)
			}
			platformsentry.SetTag(ctx, "task", job.TaskName())
			return next(ctx, job)
		}
	}
}

// LockingMiddleware 提供基于分布式锁的防并发重叠防护。
func LockingMiddleware(locker Locker, reg Registry, l *zap.Logger) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, job Job) error {
			if locker == nil {
				return next(ctx, job)
			}

			// 查询是否启用了防重叠锁
			var (
				ttl     time.Duration
				enabled bool
			)
			if r, ok := reg.(*jobRegistry); ok {
				ttl, enabled = r.LookupLockTTL(job.TaskName(), job)
			} else if olp, ok := job.(OverlappingLockProvider); ok {
				ttl, enabled = olp.WithoutOverlapping()
			}

			if !enabled {
				return next(ctx, job)
			}

			lockKey := "schedule:lock:" + job.TaskName()
			acquired, err := TryLock(ctx, locker, lockKey, ttl, func(lockCtx context.Context) error {
				return next(lockCtx, job)
			})

			if err != nil {
				return err
			}
			if !acquired {
				resolveLogger(l).Warn("scheduled task skipped due to active overlap lock",
					zap.String("task", job.TaskName()),
					zap.String("lock_key", lockKey),
				)
				return nil
			}

			return nil
		}
	}
}

// LoggingMiddleware 统计任务处理耗时并记录结构化执行日志。
func LoggingMiddleware(l *zap.Logger) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, job Job) error {
			log := resolveLogger(l)
			start := time.Now()
			err := next(ctx, job)
			duration := time.Since(start)

			if err != nil {
				fields := []zap.Field{
					zap.String("task", job.TaskName()),
					zap.Duration("duration", duration),
					zap.Error(err),
				}
				if extra := errs.OopsFields(err); len(extra) > 0 {
					fields = append(fields, extra...)
				}
				log.Error("task execution failed", fields...)
				return err
			}

			log.Debug("task execution succeeded",
				zap.String("task", job.TaskName()),
				zap.Duration("duration", duration),
			)
			return nil
		}
	}
}
