package clientauth

import (
	"context"
	"log/slog"

	"cgin/pkg/eventbus"
	"cgin/pkg/logger"
	"cgin/pkg/queue"
)

// RegisterListeners 向事件总线挂载 auth 领域切片所关注的所有领域事件监听。
func RegisterListeners(bus eventbus.Subscriber, producer queue.Producer) error {
	if bus == nil {
		return nil
	}

	return eventbus.Listen(bus, "auth.send_welcome_on_user_registered", func(ctx context.Context, evt *UserRegistered) error {
		if evt == nil {
			return nil
		}

		logger.FromContext(ctx).InfoContext(ctx, "handling UserRegistered domain event",
			slog.String("user_id", evt.UserID),
			slog.String("email", evt.Email),
		)

		if producer != nil && evt.Email != "" {
			return producer.Enqueue(ctx, TaskSendWelcomeEmail, WelcomeEmailPayload{
				Email: evt.Email,
				Name:  evt.Name,
			})
		}

		return nil
	})
}
