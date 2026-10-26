// Package mailer_test 验证邮件服务抽象、Mailpit/Resend 驱动契约与 HTML 模板渲染。
package mailer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"cgin/internal/config"
	"cgin/pkg/logger"
	"cgin/pkg/mailer"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// testMailable 模拟带模板的结构化邮件。
type testMailable struct {
	subject string
	tmpl    string
	data    map[string]any
}

func (m *testMailable) Subject() string {
	return m.subject
}

func (m *testMailable) Template() (string, map[string]any) {
	return m.tmpl, m.data
}

// mockDriver 用于单元测试模拟底层发信驱动。
type mockDriver struct {
	mu       sync.RWMutex
	messages []mailer.Message
	closed   bool
}

var _ mailer.Driver = (*mockDriver)(nil)

func (m *mockDriver) Send(_ context.Context, msg mailer.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
	return nil
}

func (m *mockDriver) Ping(ctx context.Context) error {
	return ctx.Err()
}

func (m *mockDriver) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockDriver) LastMessage() (mailer.Message, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.messages) == 0 {
		return mailer.Message{}, false
	}
	return m.messages[len(m.messages)-1], true
}

func (m *mockDriver) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.messages)
}

func TestMailpitDriverLifecycle(t *testing.T) {
	cfg := config.MailConfig{
		Driver:      "mailpit",
		FromAddress: "no-reply@cgin.local",
		FromName:    "cgin",
		Mailpit: config.MailpitConfig{
			Host: "127.0.0.1",
			Port: 1025,
		},
	}

	client, err := mailer.New(cfg, logger.Nop())
	require.NoError(t, err)
	require.NotNil(t, client)
	if mgr, ok := client.(*mailer.Manager); ok {
		assert.NotNil(t, mgr.Driver())
	}

	// 验证收件人空校验
	err = client.Send(context.Background(), mailer.Message{
		To:      nil,
		Subject: "Test",
		Body:    "Body",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recipient 'To' cannot be empty")

	// 验证安全关闭与优雅停机
	require.NoError(t, client.Close())
	require.NoError(t, client.Shutdown())
}

func TestResendDriverMissingAPIKey(t *testing.T) {
	cfg := config.MailConfig{
		Driver: "resend",
		Resend: config.ResendConfig{
			APIKey: "",
		},
	}

	client, err := mailer.New(cfg, logger.Nop())
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "api key cannot be empty")
}

func TestResendDriverSendSuccess(t *testing.T) {
	receivedAuth := ""
	receivedAgent := ""
	var receivedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedAgent = r.Header.Get("User-Agent")

		err := json.NewDecoder(r.Body).Decode(&receivedBody)
		assert.NoError(t, err)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"email_12345678"}`))
	}))
	defer server.Close()

	cfg := config.MailConfig{
		Driver:      "resend",
		FromAddress: "noreply@example.com",
		FromName:    "Shop",
		Resend: config.ResendConfig{
			APIKey:  "re_test_secret_key",
			BaseURL: server.URL,
		},
	}

	client, err := mailer.New(cfg, logger.Nop())
	require.NoError(t, err)
	require.NotNil(t, client)
	defer func() {
		require.NoError(t, client.Close())
	}()

	msg := mailer.Message{
		To:      []string{"buyer@example.com"},
		Cc:      []string{"cc@example.com"},
		Bcc:     []string{"audit@example.com"},
		Subject: "订单支付凭据",
		Body:    "您的订单已成功支付",
		IsHTML:  false,
	}

	err = client.Send(context.Background(), msg)
	require.NoError(t, err)

	assert.Equal(t, "Bearer re_test_secret_key", receivedAuth)
	assert.Equal(t, "cgin-mailer/1.0", receivedAgent)
	assert.Equal(t, "Shop <noreply@example.com>", receivedBody["from"])
	assert.Equal(t, []any{"buyer@example.com"}, receivedBody["to"])
	assert.Equal(t, "订单支付凭据", receivedBody["subject"])
	assert.Equal(t, "您的订单已成功支付", receivedBody["text"])
}

func TestResendDriverSendWithAttachments(t *testing.T) {
	tempDir := t.TempDir()
	invoiceFile := filepath.Join(tempDir, "invoice.pdf")
	require.NoError(t, os.WriteFile(invoiceFile, []byte("%PDF-1.4 Mock Invoice Content"), 0o600))

	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"email_with_attach"}`))
	}))
	defer server.Close()

	cfg := config.MailConfig{
		Driver: "resend",
		Resend: config.ResendConfig{
			APIKey:  "re_test_key",
			BaseURL: server.URL,
		},
	}

	client, err := mailer.New(cfg, logger.Nop())
	require.NoError(t, err)

	err = client.Send(context.Background(), mailer.Message{
		To:          []string{"buyer@example.com"},
		Subject:     "发票附件",
		Body:        "请查收附件",
		Attachments: []mailer.Attachment{mailer.NewAttachmentFromPath(invoiceFile)},
	})
	require.NoError(t, err)

	attachments, ok := receivedBody["attachments"].([]any)
	require.True(t, ok)
	require.Len(t, attachments, 1)

	attMap, ok := attachments[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "invoice.pdf", attMap["filename"])
	assert.NotEmpty(t, attMap["content"])
}

func TestResendDriverServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"statusCode":422,"name":"validation_error","message":"Domain not verified"}`))
	}))
	defer server.Close()

	cfg := config.MailConfig{
		Driver: "resend",
		Resend: config.ResendConfig{
			APIKey:  "re_invalid_key",
			BaseURL: server.URL,
		},
	}

	client, err := mailer.New(cfg, logger.Nop())
	require.NoError(t, err)

	err = client.Send(context.Background(), mailer.Message{
		To:      []string{"buyer@example.com"},
		Subject: "测试失败",
		Body:    "正文",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Domain not verified")
}

func TestResendDriverRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("retry-after", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"Too many requests"}`))
	}))
	defer server.Close()

	cfg := config.MailConfig{
		Driver: "resend",
		Resend: config.ResendConfig{
			APIKey:  "re_rate_limited",
			BaseURL: server.URL,
		},
	}

	client, err := mailer.New(cfg, logger.Nop())
	require.NoError(t, err)

	err = client.Send(context.Background(), mailer.Message{
		To:      []string{"buyer@example.com"},
		Subject: "测试限流",
		Body:    "正文",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limit exceeded")
}

func TestSendMailableTemplateRendering(t *testing.T) {
	var receivedHTML string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if html, ok := body["html"].(string); ok {
			receivedHTML = html
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"mailable_test"}`))
	}))
	defer server.Close()

	cfg := config.MailConfig{
		Driver: "resend",
		Resend: config.ResendConfig{
			APIKey:  "re_test_key",
			BaseURL: server.URL,
		},
	}

	client, err := mailer.New(cfg, logger.Nop())
	require.NoError(t, err)

	mailable := &testMailable{
		subject: "欢迎加入系统",
		tmpl:    "welcome",
		data: map[string]any{
			"Name": "Alex",
			"Button": map[string]any{
				"Text": "进入系统",
				"URL":  "https://example.com/dashboard",
			},
		},
	}

	err = client.SendMailable(context.Background(), "alex@example.com", mailable)
	require.NoError(t, err)
	assert.Contains(t, receivedHTML, "Alex")
	assert.Contains(t, receivedHTML, "进入系统")
	assert.Contains(t, receivedHTML, "https://example.com/dashboard")
}

func TestDefaultDriverIsMailpit(t *testing.T) {
	// 空 Driver 默认走 mailpit
	cfg := config.MailConfig{
		Driver: "",
		Mailpit: config.MailpitConfig{
			Host: "127.0.0.1",
			Port: 1025,
		},
	}

	client, err := mailer.New(cfg, logger.Nop())
	require.NoError(t, err)
	require.NotNil(t, client)
	if mgr, ok := client.(*mailer.Manager); ok {
		assert.NotNil(t, mgr.Driver())
	}
	require.NoError(t, client.Close())
}

func TestUnsupportedDriver(t *testing.T) {
	cfg := config.MailConfig{
		Driver: "sendgrid",
	}

	client, err := mailer.New(cfg, logger.Nop())
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "unsupported mail driver")
}

func TestConfigValidation(t *testing.T) {
	t.Run("mailpit合法端口", func(t *testing.T) {
		mailCfg := config.MailConfig{
			Driver: "mailpit",
			Mailpit: config.MailpitConfig{
				Host: "127.0.0.1",
				Port: 1025,
			},
		}
		require.NoError(t, mailCfg.Validate())
	})

	t.Run("mailpit非法端口", func(t *testing.T) {
		mailCfg := config.MailConfig{
			Driver: "mailpit",
			Mailpit: config.MailpitConfig{
				Port: 99999,
			},
		}
		err := mailCfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mailpit port must be between 1 and 65535")
	})

	t.Run("resend缺少APIKey", func(t *testing.T) {
		mailCfg := config.MailConfig{
			Driver: "resend",
			Resend: config.ResendConfig{
				APIKey: "",
			},
		}
		err := mailCfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resend api key cannot be empty")
	})

	t.Run("不支持的驱动报错", func(t *testing.T) {
		mailCfg := config.MailConfig{
			Driver: "unknown_driver",
		}
		err := mailCfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported mail driver")
	})
}

func TestAttachmentAndFluentMessage(t *testing.T) {
	// 临时文件用于路径附件测试
	tmpFile := filepath.Join(t.TempDir(), "test.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("disk content"), 0o600))

	t.Run("内存附件与路径附件 Bytes", func(t *testing.T) {
		memAtt := mailer.NewAttachmentFromBytes("doc.pdf", []byte("pdf data"), "application/pdf")
		data, err := memAtt.Bytes()
		require.NoError(t, err)
		assert.Equal(t, []byte("pdf data"), data)
		assert.Equal(t, "doc.pdf", memAtt.Filename)
		assert.Equal(t, "application/pdf", memAtt.ContentType)

		fileAtt := mailer.NewAttachmentFromPath(tmpFile)
		data, err = fileAtt.Bytes()
		require.NoError(t, err)
		assert.Equal(t, []byte("disk content"), data)
		assert.Equal(t, "test.txt", fileAtt.Filename)

		emptyAtt := mailer.Attachment{}
		_, err = emptyAtt.Bytes()
		require.Error(t, err)
	})

	t.Run("Fluent Message 链式构建", func(t *testing.T) {
		msg := mailer.NewMessage().
			WithTo("alice@example.com", "bob@example.com").
			WithCc("charlie@example.com").
			WithBcc("audit@example.com").
			WithSubject("Invoice Notice").
			WithHTML("<h1>Invoice</h1>").
			WithAttachmentFile(tmpFile).
			WithAttachmentBytes("sample.txt", []byte("hello world"))

		assert.Equal(t, []string{"alice@example.com", "bob@example.com"}, msg.To)
		assert.Equal(t, "alice@example.com", msg.PrimaryRecipient())
		assert.Equal(t, []string{"charlie@example.com"}, msg.Cc)
		assert.Equal(t, []string{"audit@example.com"}, msg.Bcc)
		assert.Equal(t, "Invoice Notice", msg.Subject)
		assert.True(t, msg.IsHTML)
		assert.Len(t, msg.Attachments, 2)
		assert.Len(t, msg.AllAttachments(), 2)
	})
}

func TestManager_SendAndLifecycle(t *testing.T) {
	t.Run("有效消息正常投递至驱动", func(t *testing.T) {
		mock := &mockDriver{}
		mgr := mailer.NewManager(mock)

		err := mgr.Send(context.Background(), mailer.NewMessage().
			WithTo("user@example.com").
			WithSubject("Test").
			WithText("Hello World"))
		require.NoError(t, err)

		last, ok := mock.LastMessage()
		require.True(t, ok)
		assert.Equal(t, []string{"user@example.com"}, last.To)
		assert.Equal(t, "Test", last.Subject)
		assert.Equal(t, "Hello World", last.Body)
	})

	t.Run("缺失收件人拒绝投递", func(t *testing.T) {
		mock := &mockDriver{}
		mgr := mailer.NewManager(mock)

		err := mgr.Send(context.Background(), mailer.NewMessage().WithSubject("Test"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "recipient 'To' cannot be empty")
	})

	t.Run("驱动未初始化报错", func(t *testing.T) {
		mgr := mailer.NewManager(nil)

		err := mgr.Send(context.Background(), mailer.NewMessage().WithTo("user@example.com"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "driver is not initialized")
	})
}

func TestCustomDriverRegistration(t *testing.T) {
	mock := &mockDriver{}
	mailer.RegisterDriver("custom-test", func() (mailer.Driver, error) {
		return mock, nil
	})

	client, err := mailer.New(config.MailConfig{Driver: "custom-test"}, logger.Nop())
	require.NoError(t, err)
	require.NotNil(t, client)

	err = client.Send(context.Background(), mailer.NewMessage().WithTo("test@custom.com").WithSubject("Custom"))
	require.NoError(t, err)
	assert.Equal(t, 1, mock.Count())
}

func BenchmarkMessage_FluentBuilder(b *testing.B) {
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = mailer.NewMessage().
			WithTo("alice@example.com", "bob@example.com").
			WithCc("charlie@example.com").
			WithSubject("Bench Subject").
			WithHTML("<p>Bench Body</p>")
	}
}

func BenchmarkManager_Send(b *testing.B) {
	mock := &mockDriver{}
	mgr := mailer.NewManager(mock)
	ctx := context.Background()
	msg := mailer.NewMessage().
		WithTo("user@example.com").
		WithSubject("Bench").
		WithText("Bench text")

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = mgr.Send(ctx, msg)
	}
}
