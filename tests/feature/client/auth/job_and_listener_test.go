package auth_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/internal/event"
	"cgin/internal/job"
	"cgin/internal/listener"
	"cgin/pkg/eventbus"
	"cgin/pkg/logger"
	"cgin/pkg/mailer"
	"cgin/pkg/queue"
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

func (m *mockMailer) Shutdown() error {
	return m.Close()
}

func (m *mockMailer) Ping(_ context.Context) error {
	return nil
}

func TestWelcomeEmailJobExecution(t *testing.T) {
	t.Parallel()

	mailMock := &mockMailer{}
	welcomeEmail := job.NewWelcomeEmailJob("alex@example.com", "Alex")

	assert.Equal(t, job.TaskSendWelcomeEmail, welcomeEmail.TaskName())
	assert.Equal(t, queue.DefaultQueueName, welcomeEmail.QueueName())
	assert.Equal(t, 3, welcomeEmail.MaxRetries())

	reg := queue.DefaultRegistry()
	job.RegisterJobs(reg, mailMock)

	err := welcomeEmail.Handle(t.Context())
	require.NoError(t, err) // 未注入 mailer 时优雅跳过

	// 验证从注册表工厂还原的任务具有注入的 mailer 并能成功渲染发送
	factory, ok := reg.Get(job.TaskSendWelcomeEmail)
	require.True(t, ok)
	injectedJob := factory()
	injectedWelcome, ok := injectedJob.(*job.WelcomeEmailJob)
	require.True(t, ok)
	injectedWelcome.Email = "alex@example.com"
	injectedWelcome.Name = "Alex"

	err = injectedWelcome.Handle(t.Context())
	require.NoError(t, err)
	require.Len(t, mailMock.sent, 1)
	assert.Equal(t, []string{"alex@example.com"}, mailMock.sent[0].To)
	assert.Contains(t, mailMock.sent[0].Body, "Alex")
	assert.Contains(t, mailMock.sent[0].Body, "Welcome to cgin")
}

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

	err = listener.RegisterListeners(bus, dispatcher)
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

	evt := &event.UserRegistered{
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

	dispatcher.AssertDispatched(t, job.TaskSendWelcomeEmail, 1)
	dispatcher.AssertDispatchedJob(t, func(j queue.Job) bool {
		if welcomeJob, ok := j.(*job.WelcomeEmailJob); ok {
			return welcomeJob.Email == "user@example.com" && welcomeJob.Name == "John Doe"
		}
		return false
	})
}
