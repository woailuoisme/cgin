package auth_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/features/client/auth"
	"bgin/internal/platform/eventbus"
	"bgin/internal/platform/mailer"
	"bgin/internal/platform/queue"
	"bgin/internal/shared/events"
)

type mockMailer struct {
	mu   sync.Mutex
	sent []mailer.Message
}

func (m *mockMailer) Send(_ context.Context, msg mailer.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mockMailer) SendMailable(_ context.Context, _ string, _ mailer.Mailable) error {
	return nil
}

func (m *mockMailer) Close() error {
	return nil
}

func (m *mockMailer) Ping(_ context.Context) error {
	return nil
}

func TestWelcomeEmailJobExecution(t *testing.T) {
	t.Parallel()

	mailMock := &mockMailer{}
	job := auth.NewWelcomeEmailJob("alex@example.com", "Alex")

	assert.Equal(t, auth.TaskSendWelcomeEmail, job.TaskName())
	assert.Equal(t, queue.DefaultQueueName, job.QueueName())
	assert.Equal(t, 3, job.MaxRetries())

	reg := queue.DefaultRegistry()
	auth.RegisterJobs(reg, mailMock)

	err := job.Handle(t.Context())
	require.NoError(t, err) // 未注入 mailer 时优雅跳过

	// 验证从注册表工厂还原的任务具有注入的 mailer 并能成功渲染发送
	factory, ok := reg.Get(auth.TaskSendWelcomeEmail)
	require.True(t, ok)
	injectedJob := factory()
	welcomeJob, ok := injectedJob.(*auth.WelcomeEmailJob)
	require.True(t, ok)
	welcomeJob.Email = "alex@example.com"
	welcomeJob.Name = "Alex"

	err = welcomeJob.Handle(t.Context())
	require.NoError(t, err)
	require.Len(t, mailMock.sent, 1)
	assert.Equal(t, "alex@example.com", mailMock.sent[0].To)
	assert.Contains(t, mailMock.sent[0].Body, "Alex")
	assert.Contains(t, mailMock.sent[0].Body, "Welcome to bgin")
}

func TestAuthEventListener(t *testing.T) {
	t.Parallel()

	dispatcher := queue.NewMemoryDispatcher()

	cfg := config.EventBusConfig{
		Driver:   eventbus.DriverGoChannel,
		LogLevel: "error",
	}
	bus, err := eventbus.NewManager(cfg, zap.NewNop())
	require.NoError(t, err)
	defer func() { _ = bus.Close() }()

	err = auth.RegisterListeners(bus, dispatcher)
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

	evt := &events.UserRegistered{
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

	dispatcher.AssertDispatched(t, auth.TaskSendWelcomeEmail, 1)
	dispatcher.AssertDispatchedJob(t, func(j queue.Job) bool {
		if welcomeJob, ok := j.(*auth.WelcomeEmailJob); ok {
			return welcomeJob.Email == "user@example.com" && welcomeJob.Name == "John Doe"
		}
		return false
	})
}
