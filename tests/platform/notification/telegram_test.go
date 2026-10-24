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

	"bgin/internal/platform/notification/channel/chat"
	"bgin/internal/platform/notification/channel/chat/telegram"
)

// telegramStub 记录 Telegram Bot API 收到的一次请求。
// 每个子测试各建一份实例，避免用共享变量在子测试间传递状态（并行化即失效）。
type telegramStub struct {
	mu   sync.Mutex
	path string
	body map[string]any
}

// snapshot 返回最近一次请求的路径与请求体。
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

// startTelegramStub 用 httptest 顶替真实 Bot API（走真实 HTTP 客户端，不 mock 传输层）。
func startTelegramStub(t *testing.T) (baseURL string, stub *telegramStub, closeFn func()) {
	t.Helper()

	stub = &telegramStub{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.record(r)

		// resty 只有在响应声明 JSON 时才会把 body 反序列化进 SetResult 的目标结构体。
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

// newTestDriver 构造指向桩服务的驱动；chatID 为空表示驱动未配置默认会话。
func newTestDriver(t *testing.T, baseURL, chatID string) *telegram.Driver {
	t.Helper()

	driver, err := telegram.NewDriver(telegram.Config{
		BotToken: "TEST_TOKEN",
		ChatID:   chatID,
		BaseURL:  baseURL,
	})
	require.NoError(t, err)
	return driver
}

// TestTelegramDriverSend 覆盖 Telegram 驱动的请求装配与成功分支。
// 这是唯一无独立单测的第三方通知驱动，用 httptest 顶替真实 Bot API。
func TestTelegramDriverSend(t *testing.T) {
	baseURL, stub, closeStub := startTelegramStub(t)
	defer closeStub()

	driver := newTestDriver(t, baseURL, "default-chat")
	assert.Equal(t, "telegram", driver.Name())

	msg := &chat.Message{Text: "deploy finished", ParseMode: "Markdown"}
	require.NoError(t, driver.Send(t.Context(), msg))

	path, body := stub.snapshot()
	assert.Equal(t, "/botTEST_TOKEN/sendMessage", path)
	// 未指定 Recipient 时回退到驱动配置的默认会话。
	assert.Equal(t, "default-chat", body["chat_id"])
	assert.Equal(t, "deploy finished", body["text"])
	assert.Equal(t, "Markdown", body["parse_mode"])
}

// TestTelegramDriverRecipientOverridesDefault 校验消息自带接收方时优先于驱动默认会话。
func TestTelegramDriverRecipientOverridesDefault(t *testing.T) {
	baseURL, stub, closeStub := startTelegramStub(t)
	defer closeStub()

	driver := newTestDriver(t, baseURL, "default-chat")
	require.NoError(t, driver.Send(t.Context(), &chat.Message{Recipient: "-100200", Text: "hi"}))

	_, body := stub.snapshot()
	assert.Equal(t, "-100200", body["chat_id"])
}

// TestTelegramDriverRejectsInvalidInput 校验缺失凭据与必填字段一律拒绝。
func TestTelegramDriverRejectsInvalidInput(t *testing.T) {
	baseURL, _, closeStub := startTelegramStub(t)
	defer closeStub()

	_, err := telegram.NewDriver(telegram.Config{})
	require.Error(t, err)

	// 驱动无默认会话且消息未带接收方：必须在出站前失败。
	noChat := newTestDriver(t, baseURL, "")
	require.Error(t, noChat.Send(t.Context(), &chat.Message{Text: "hi"}))

	require.Error(t, newTestDriver(t, baseURL, "chat").Send(t.Context(), &chat.Message{}))
}

// TestTelegramDriverContextCanceled 校验 ctx 被真正传导到出站 HTTP 调用。
func TestTelegramDriverContextCanceled(t *testing.T) {
	baseURL, _, closeStub := startTelegramStub(t)
	defer closeStub()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.Error(t, newTestDriver(t, baseURL, "chat").Send(ctx, &chat.Message{Text: "hi"}))
}

// TestTelegramDriverUpstreamError 校验上游非 2xx 时返回可识别错误。
func TestTelegramDriverUpstreamError(t *testing.T) {
	baseURL, _, closeStub := startTelegramStub(t)
	defer closeStub()

	// 在 BaseURL 上追加错误前缀，令桩服务返回 404。
	broken := newTestDriver(t, baseURL+"/bad-prefix", "chat")

	err := broken.Send(t.Context(), &chat.Message{Text: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "telegram delivery failed")
}
