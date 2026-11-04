package job_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/job"
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

func TestWelcomeEmailHandler(t *testing.T) {
	t.Parallel()

	mailMock := &mockMailer{}
	handler := job.NewWelcomeEmailHandler(mailMock)
	payload := job.WelcomeEmailPayload{
		Email: "alex@example.com",
		Name:  "Alex",
	}

	router := queue.NewTaskRouter()
	job.RegisterJobs(router, mailMock)

	taskHandler, ok := router.GetHandler(job.TaskSendWelcomeEmail)
	require.True(t, ok)
	require.NotNil(t, taskHandler)

	err := handler.Handle(t.Context(), payload)
	require.NoError(t, err)
	require.Len(t, mailMock.sent, 1)
	assert.Equal(t, []string{"alex@example.com"}, mailMock.sent[0].To)
	assert.Contains(t, mailMock.sent[0].Body, "Alex")
	assert.Contains(t, mailMock.sent[0].Body, "Welcome to cgin")

	// 验证未注入 mailer 时优雅跳过
	emptyHandler := job.NewWelcomeEmailHandler(nil)
	require.NoError(t, emptyHandler.Handle(t.Context(), payload))
}
