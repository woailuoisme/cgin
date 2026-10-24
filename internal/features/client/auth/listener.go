package auth

import (
	"context"

	"go.uber.org/zap"

	"bgin/internal/platform/eventbus"
	"bgin/internal/platform/logger"
	"bgin/internal/platform/queue"
	"bgin/internal/shared/events"
)

// RegisterListeners 向事件总线挂载 auth 领域切片所关注的所有领域事件监听。
func RegisterListeners(bus eventbus.Subscriber, dispatcher queue.Dispatcher) error {
	if bus == nil {
		return nil
	}

	return eventbus.Listen(bus, "auth.send_welcome_on_user_registered", func(ctx context.Context, evt *events.UserRegistered) error {
		if evt == nil {
			return nil
		}

		logger.FromContext(ctx).Info("handling UserRegistered domain event",
			zap.String("user_id", evt.UserID),
			zap.String("email", evt.Email),
		)

		if dispatcher != nil && evt.Email != "" {
			job := NewWelcomeEmailJob(evt.Email, evt.Name)
			return dispatcher.Dispatch(ctx, job)
		}

		return nil
	})
}
