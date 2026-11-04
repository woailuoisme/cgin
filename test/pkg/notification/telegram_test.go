package notification_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/pkg/notification"
)

// telegramStub 记录 Telegram Bot API 收到的一次请求。
type telegramStub struct {
	mu   sync.Mutex
	path string
	body map[string]any
}

func (s *telegramStub) snapshot() (string, map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path, s.body
}

func (s *telegramStub) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.path = r.URL.Path
	s.body = nil
	raw, err := io.ReadAll(r.Body)
	if err == nil {
		_ = json.Unmarshal(raw, &s.body)
	}
}

func startTelegramStub(t *testing.T) (baseURL string, stub *telegramStub, closeFn func()) {
	t.Helper()

	stub = &telegramStub{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.record(r)

		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/botTEST_TOKEN/sendMessage" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":404,"description":"Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	return server.URL, stub, server.Close
}

func newTestDriver(t *testing.T, baseURL, chatID string) *notification.TelegramDriver {
	t.Helper()

	driver, err := notification.NewTelegramDriver(notification.TelegramConfig{
		BotToken: "TEST_TOKEN",
		ChatID:   chatID,
		BaseURL:  baseURL,
	})
	require.NoError(t, err)
	return driver
}

func TestTelegramDriverSend(t *testing.T) {
	baseURL, stub, closeStub := startTelegramStub(t)
	defer closeStub()

	driver := newTestDriver(t, baseURL, "default-chat")
	assert.Equal(t, "telegram", driver.Name())

	msg := &notification.ChatMessage{Text: "deploy finished", ParseMode: "Markdown"}
	require.NoError(t, driver.Send(t.Context(), msg))

	path, body := stub.snapshot()
	assert.Equal(t, "/botTEST_TOKEN/sendMessage", path)
	assert.Equal(t, "default-chat", body["chat_id"])
	assert.Equal(t, "deploy finished", body["text"])
	assert.Equal(t, "Markdown", body["parse_mode"])
}

func TestTelegramDriverRecipientOverridesDefault(t *testing.T) {
	baseURL, stub, closeStub := startTelegramStub(t)
	defer closeStub()

	driver := newTestDriver(t, baseURL, "default-chat")
	require.NoError(t, driver.Send(t.Context(), &notification.ChatMessage{Recipient: "-100200", Text: "hi"}))

	_, body := stub.snapshot()
	assert.Equal(t, "-100200", body["chat_id"])
}

func TestTelegramDriverRejectsInvalidInput(t *testing.T) {
	baseURL, _, closeStub := startTelegramStub(t)
	defer closeStub()

	_, err := notification.NewTelegramDriver(notification.TelegramConfig{})
	require.Error(t, err)

	noChat := newTestDriver(t, baseURL, "")
	require.Error(t, noChat.Send(t.Context(), &notification.ChatMessage{Text: "hi"}))

	require.Error(t, newTestDriver(t, baseURL, "chat").Send(t.Context(), &notification.ChatMessage{}))
}

func TestTelegramDriverContextCanceled(t *testing.T) {
	baseURL, _, closeStub := startTelegramStub(t)
	defer closeStub()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.Error(t, newTestDriver(t, baseURL, "chat").Send(ctx, &notification.ChatMessage{Text: "hi"}))
}

func TestTelegramDriverUpstreamError(t *testing.T) {
	baseURL, _, closeStub := startTelegramStub(t)
	defer closeStub()

	broken := newTestDriver(t, baseURL+"/bad-prefix", "chat")

	err := broken.Send(t.Context(), &notification.ChatMessage{Text: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "telegram delivery failed")
}
