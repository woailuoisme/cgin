package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	clientauth "cgin/internal/feature/client/auth"
	"cgin/pkg/eventbus"
	"cgin/pkg/logger"
	"cgin/pkg/queue"
)

func TestAuthEventListener(t *testing.T) {
	t.Parallel()

	dispatcher := queue.NewMemoryDispatcher()

	cfg := config.EventBusConfig{
		Driver:   eventbus.DriverGoChannel,
		LogLevel: "error",
	}
	bus, err := eventbus.NewManager(cfg, logger.Nop())
	require.NoError(t, err)
	defer func() { _ = bus.Close() }()

	err = clientauth.RegisterListeners(bus, dispatcher)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- bus.Run(ctx)
	}()

	// 等待事件路由器准备就绪
	select {
	case <-bus.Running():
	case <-time.After(3 * time.Second):
		t.Fatal("eventbus router did not become ready in time")
	}

	evt := &clientauth.UserRegistered{
		UserID:       "usr_123",
		Email:        "user@example.com",
		Name:         "John Doe",
		RegisteredAt: time.Now(),
	}

	err = bus.Publish(t.Context(), evt)
	require.NoError(t, err)

	// 轮询验证 Dispatcher 收到派发的任务
	require.Eventually(t, func() bool {
		return dispatcher.Count() > 0
	}, 3*time.Second, 50*time.Millisecond)

	dispatcher.AssertDispatched(t, clientauth.TaskSendWelcomeEmail, 1)
	dispatcher.AssertDispatchedTask(t, func(task queue.DispatchedTask) bool {
		if payload, ok := task.Payload.(clientauth.WelcomeEmailPayload); ok {
			return payload.Email == "user@example.com" && payload.Name == "John Doe"
		}
		return false
	})
}
