package service

import (
	"context"

	"cgin/pkg/eventbus"
	"cgin/pkg/logger"
	"cgin/pkg/queue"
	"cgin/shared/events"
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

		logger.FromContext(ctx).Info().
			Str("user_id", evt.UserID).
			Str("email", evt.Email).
			Msg("handling UserRegistered domain event")

		if dispatcher != nil && evt.Email != "" {
			job := NewWelcomeEmailJob(evt.Email, evt.Name)
			return dispatcher.Dispatch(ctx, job)
		}

		return nil
	})
}
