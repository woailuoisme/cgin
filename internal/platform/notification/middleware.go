// Package notification 提供统一、模块化、高可扩展的多渠道通知基础设施。
package notification

import (
	"context"
	"slices"
	"time"

	"github.com/samber/oops"
	"go.uber.org/zap"

	"bgin/internal/platform/cache"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/logger"
	"bgin/internal/platform/notification/contract"
)

// SendFunc 定义通知投递管道处理函数契约。
type SendFunc func(ctx context.Context, target any, notif contract.Notification, channel string) error

// Middleware 定义通知管道中间件。
type Middleware func(next SendFunc) SendFunc

// WrapMiddleware 按照洋葱圈模型依次回调组装中间件切片。
func WrapMiddleware(handler SendFunc, middlewares ...Middleware) SendFunc {
	for _, mw := range slices.Backward(middlewares) {
		if mw != nil {
			handler = mw(handler)
		}
	}
	return handler
}

// RecoveryMiddleware 捕获管道中的 panic 并在日志中记录，返回结构化错误。
func RecoveryMiddleware() Middleware {
	return func(next SendFunc) SendFunc {
		return func(ctx context.Context, target any, notif contract.Notification, channel string) (err error) {
			defer func() {
				if r := recover(); r != nil {
					logger.FromContext(ctx).Error("panic recovered in notification pipeline",
						zap.Any("panic", r),
						zap.String("channel", channel))
					err = oops.Code(errs.CodeNotificationPanic).
						With("channel", channel).
						Errorf("notification panic: %v", r)
				}
			}()
			return next(ctx, target, notif, channel)
		}
	}
}

// RateLimitMiddleware 基于缓存存储的原子计数进行通道/目标维度的频控拦截。
// 若 limit <= 0 或 window <= 0 则跳过频控检测。
func RateLimitMiddleware(store cache.Store, limit int, window time.Duration) Middleware {
	return func(next SendFunc) SendFunc {
		return func(ctx context.Context, target any, notif contract.Notification, channel string) error {
			if store == nil || limit <= 0 || window <= 0 {
				return next(ctx, target, notif, channel)
			}

			var rateKey string
			if rl, ok := notif.(contract.RateLimitedNotification); ok {
				rateKey = rl.RateLimitKey(channel, target)
			}

			if rateKey == "" {
				notifiable := contract.ToNotifiable(target)
				targetID := notifiable.GetNotifiableID()
				if targetID != "" {
					rateKey = cache.Key("notification", "ratelimit", channel, targetID)
				}
			}

			if rateKey != "" {
				count, err := store.Increment(ctx, rateKey, window)
				if err == nil && count > int64(limit) {
					return oops.Code(errs.CodeNotificationRateLimited).
						With("channel", channel).
						With("key", rateKey).
						With("count", count).
						With("limit", limit).
						Wrap(contract.ErrRateLimited)
				}
			}

			return next(ctx, target, notif, channel)
		}
	}
}

// DeduplicateMiddleware 对实现了 IdempotentNotification 的通知执行防重校验。
// 若未获取到唯一键或 ttl <= 0 则跳过防重检测。
func DeduplicateMiddleware(store cache.Store, defaultTTL time.Duration) Middleware {
	return func(next SendFunc) SendFunc {
		return func(ctx context.Context, target any, notif contract.Notification, channel string) error {
			if store == nil || defaultTTL <= 0 {
				return next(ctx, target, notif, channel)
			}

			idempotent, ok := notif.(contract.IdempotentNotification)
			if !ok {
				return next(ctx, target, notif, channel)
			}

			key := idempotent.IdempotentKey(target)
			if key == "" {
				return next(ctx, target, notif, channel)
			}

			dedupKey := cache.Key("notification", "idempotent", channel, key)
			acquired, err := store.SetNX(ctx, dedupKey, "1", defaultTTL)
			if err != nil {
				logger.FromContext(ctx).Warn("failed to verify notification idempotency, proceeding",
					zap.Error(err),
					zap.String("channel", channel),
					zap.String("key", dedupKey))
			} else if !acquired {
				return oops.Code(errs.CodeNotificationDeduplicated).
					With("channel", channel).
					With("key", dedupKey).
					Wrap(contract.ErrDeduplicated)
			}

			return next(ctx, target, notif, channel)
		}
	}
}
