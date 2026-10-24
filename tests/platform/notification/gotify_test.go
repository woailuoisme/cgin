// Package notification_test 针对 Gotify 渠道、模型、HTTP 驱动与 Mock 驱动进行全方位单元契约测试。
package notification_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/notification"
	"bgin/internal/platform/notification/channel/gotify"
	gotifyHTTP "bgin/internal/platform/notification/channel/gotify/http"
	gotifyMock "bgin/internal/platform/notification/channel/gotify/mock"
)

// TestGotifyMessage_FluentAPIs 验证 Gotify 消息模型的链式构建与高级特性。
func TestGotifyMessage_FluentAPIs(t *testing.T) {
	t.Parallel()

	msg := gotify.NewMessage("Database backup completed").
		WithTitle("System Alert").
		HighPriority().
		Markdown().
		WithClickURL("https://admin.example.com/backups").
		WithExtra("custom_id", "bk-12345").
		WithToken("app-token-custom").
		WithServerURL("https://gotify.custom.net")

	assert.Equal(t, "System Alert", msg.Title)
	assert.Equal(t, "Database backup completed", msg.Message)
	assert.Equal(t, 8, msg.Priority)
	assert.Equal(t, "app-token-custom", msg.Token)
	assert.Equal(t, "https://gotify.custom.net", msg.ServerURL)

	// 验证 Markdown 扩展
	display, ok := msg.Extras[gotify.ExtraKeyClientDisplay].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "text/markdown", display["contentType"])

	// 验证 ClickURL 扩展
	notif, ok := msg.Extras[gotify.ExtraKeyClientNotification].(map[string]any)
	require.True(t, ok)
	click, ok := notif["click"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://admin.example.com/backups", click["url"])

	assert.Equal(t, "bk-12345", msg.Extras["custom_id"])

	// 验证优先级方法
	assert.Equal(t, 10, msg.UrgentPriority().Priority)
	assert.Equal(t, 5, msg.NormalPriority().Priority)
	assert.Equal(t, 2, msg.LowPriority().Priority)
	assert.Equal(t, 0, msg.MinPriority().Priority)

	// 验证清除 Markdown
	msg.WithMarkdown(false)
	assert.Nil(t, msg.Extras[gotify.ExtraKeyClientDisplay])

	// 验证清除 ClickURL
	msg.WithClickURL("")
	assert.Nil(t, msg.Extras[gotify.ExtraKeyClientNotification])
}

type mockGotifyNotification struct {
	title   string
	content string
}

func (n *mockGotifyNotification) Via(_ any) []string {
	return []string{"gotify"}
}

func (n *mockGotifyNotification) ToGotify(_ any) any {
	return gotify.NewMessage(n.content).
		WithTitle(n.title).
		UrgentPriority()
}

// TestGotifyChannel_FormatAndDispatch 验证渠道格式化与 Mock 驱动分发链路。
func TestGotifyChannel_FormatAndDispatch(t *testing.T) {
	t.Parallel()

	mockDriver := gotifyMock.NewDriver()
	ch := gotify.NewChannel("mock").
		WithDefaults("https://gotify.example.com", "default-app-token", 5)
	ch.RegisterDriver("mock", mockDriver)

	ctx := context.Background()

	t.Run("实现 gotify.Notification 契约格式化与发送", func(t *testing.T) {
		mockDriver.Clear()
		notif := &mockGotifyNotification{
			title:   "磁盘空间不足",
			content: "服务器 /data 分区使用率已达 92%",
		}

		formatted, err := ch.Format(notif, nil)
		require.NoError(t, err)

		err = ch.Send(ctx, nil, formatted)
		require.NoError(t, err)

		lastMsg := mockDriver.GetLastMessage()
		require.NotNil(t, lastMsg)
		assert.Equal(t, "磁盘空间不足", lastMsg.Title)
		assert.Equal(t, "服务器 /data 分区使用率已达 92%", lastMsg.Message)
		assert.Equal(t, 10, lastMsg.Priority)
		assert.Equal(t, "https://gotify.example.com", lastMsg.ServerURL)
		assert.Equal(t, "default-app-token", lastMsg.Token)
	})

	t.Run("三级 Token 与 ServerURL 寻址优先级", func(t *testing.T) {
		mockDriver.Clear()

		// 1. 接收者路由包含完整 Webhook URL
		routeUser := notification.NewRecipient("u-1", "user").
			WithRoute("gotify", "https://gotify.user.io/message?token=user-specific-token")

		msg := gotify.NewMessage("User specific message")
		err := ch.Send(ctx, routeUser, msg)
		require.NoError(t, err)

		lastMsg := mockDriver.GetLastMessage()
		require.NotNil(t, lastMsg)
		assert.Equal(t, "https://gotify.user.io", lastMsg.ServerURL)
		assert.Equal(t, "user-specific-token", lastMsg.Token)

		// 2. 接收者路由仅包含纯 Token
		mockDriver.Clear()
		routeTokenUser := notification.NewRecipient("u-2", "user").
			WithRoute("gotify", "pure-token-666")

		msg2 := gotify.NewMessage("Token message")
		err = ch.Send(ctx, routeTokenUser, msg2)
		require.NoError(t, err)

		lastMsg2 := mockDriver.GetLastMessage()
		require.NotNil(t, lastMsg2)
		assert.Equal(t, "https://gotify.example.com", lastMsg2.ServerURL)
		assert.Equal(t, "pure-token-666", lastMsg2.Token)

		// 3. Message 显式覆盖优先级最高
		mockDriver.Clear()
		msg3 := gotify.NewMessage("Explicit override").
			WithServerURL("https://override.net").
			WithToken("override-token")

		err = ch.Send(ctx, routeTokenUser, msg3)
		require.NoError(t, err)

		lastMsg3 := mockDriver.GetLastMessage()
		require.NotNil(t, lastMsg3)
		assert.Equal(t, "https://override.net", lastMsg3.ServerURL)
		assert.Equal(t, "override-token", lastMsg3.Token)
	})

	t.Run("SendSimple 快捷直接推送", func(t *testing.T) {
		mockDriver.Clear()
		err := ch.SendSimple(ctx, "系统启动", "所有微服务集群已就绪", 6)
		require.NoError(t, err)

		last := mockDriver.GetLastMessage()
		require.NotNil(t, last)
		assert.Equal(t, "系统启动", last.Title)
		assert.Equal(t, "所有微服务集群已就绪", last.Message)
		assert.Equal(t, 6, last.Priority)
	})
}

// TestGotifyHTTPDriver_Send 验证通过 REST API 发送消息给真实模拟服务端。
func TestGotifyHTTPDriver_Send(t *testing.T) {
	t.Parallel()

	var (
		receivedAuthToken string
		receivedPath      string
		receivedBody      map[string]any
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthToken = r.Header.Get("X-Gotify-Key")
		receivedPath = r.URL.Path

		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &receivedBody)

		if receivedAuthToken != "valid-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Unauthorized","errorCode":401,"errorDescription":"invalid token"}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":100,"appid":1,"message":"ok","priority":5}`))
	}))
	defer server.Close()

	ctx := context.Background()

	driver, err := gotifyHTTP.NewDriver(gotifyHTTP.Config{
		ServerURL: server.URL,
		AppToken:  "valid-token",
		Priority:  5,
		Timeout:   2 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, "http", driver.Name())

	t.Run("正常推送发送", func(t *testing.T) {
		msg := gotify.NewMessage("Hello from bgin!").
			WithTitle("Greeting").
			HighPriority().
			Markdown().
			WithClickURL("https://bgin.dev")

		err := driver.Send(ctx, msg)
		require.NoError(t, err)

		assert.Equal(t, "valid-token", receivedAuthToken)
		assert.Equal(t, "/message", receivedPath)
		assert.Equal(t, "Greeting", receivedBody["title"])
		assert.Equal(t, "Hello from bgin!", receivedBody["message"])
		assert.InDelta(t, 8.0, receivedBody["priority"], 0.001)

		extras, ok := receivedBody["extras"].(map[string]any)
		require.True(t, ok)
		assert.NotNil(t, extras["client::display"])
		assert.NotNil(t, extras["client::notification"])
	})

	t.Run("Token 错误导致 401 失败", func(t *testing.T) {
		invalidDriver, _ := gotifyHTTP.NewDriver(gotifyHTTP.Config{
			ServerURL: server.URL,
			AppToken:  "wrong-token",
		})

		msg := gotify.NewMessage("Fail test")
		err := invalidDriver.Send(ctx, msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gotify notification delivery failed")
	})

	t.Run("缺少必填参数校验拦截", func(t *testing.T) {
		emptyDriver, _ := gotifyHTTP.NewDriver(gotifyHTTP.Config{})

		// 缺少 ServerURL
		err := emptyDriver.Send(ctx, gotify.NewMessage("Test"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "server_url is required")

		// 缺少 Token
		err = emptyDriver.Send(ctx, gotify.NewMessage("Test").WithServerURL("https://gotify.net"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "app_token is required")

		// 缺少 Message 内容
		err = emptyDriver.Send(ctx, gotify.NewMessage("").WithServerURL("https://gotify.net").WithToken("tok"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "message text is required")
	})
}

// TestNotificationGotifyAssembly 验证 Notification 统一工厂自动装配 Gotify 渠道。
func TestNotificationGotifyAssembly(t *testing.T) {
	t.Parallel()

	cfg := config.NotificationConfig{
		WorkerPool: 2,
		QueueSize:  20,
		Gotify: config.NotificationGotifyConfig{
			ServerURL: "https://gotify.myorg.internal",
			AppToken:  "secret-app-token",
			Priority:  7,
		},
	}

	notifier, err := notification.New(cfg, nil, nil)
	require.NoError(t, err)
	defer func() {
		_ = notifier.Close()
	}()

	ch := notifier.Channel("gotify")
	require.NotNil(t, ch)
	assert.Equal(t, "gotify", ch.Name())
}
