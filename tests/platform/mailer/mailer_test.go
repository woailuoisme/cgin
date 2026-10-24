// Package mailer_test 验证邮件服务抽象、Mailpit/Resend 驱动契约与 HTML 模板渲染。
package mailer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/mailer"
)

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

func TestMailpitDriverLifecycle(t *testing.T) {
	cfg := config.MailConfig{
		Driver:      "mailpit",
		FromAddress: "no-reply@bgin.local",
		FromName:    "bgin",
		Mailpit: config.MailpitConfig{
			Host: "127.0.0.1",
			Port: 1025,
		},
	}

	client, err := mailer.New(cfg, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, client)

	// 验证收件人空校验
	err = client.Send(context.Background(), mailer.Message{
		To:      "",
		Subject: "Test",
		Body:    "Body",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recipient 'To' cannot be empty")

	// 验证安全关闭
	require.NoError(t, client.Close())
}

func TestResendDriverMissingAPIKey(t *testing.T) {
	cfg := config.MailConfig{
		Driver: "resend",
		Resend: config.ResendConfig{
			APIKey: "",
		},
	}

	client, err := mailer.New(cfg, zap.NewNop())
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

	client, err := mailer.New(cfg, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, client)
	defer func() {
		require.NoError(t, client.Close())
	}()

	msg := mailer.Message{
		To:      "buyer@example.com",
		Cc:      []string{"cc@example.com"},
		Bcc:     []string{"audit@example.com"},
		Subject: "订单支付凭据",
		Body:    "您的订单已成功支付",
		IsHTML:  false,
	}

	err = client.Send(context.Background(), msg)
	require.NoError(t, err)

	assert.Equal(t, "Bearer re_test_secret_key", receivedAuth)
	assert.Equal(t, "bgin-mailer/1.0", receivedAgent)
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

	client, err := mailer.New(cfg, zap.NewNop())
	require.NoError(t, err)

	err = client.Send(context.Background(), mailer.Message{
		To:          "buyer@example.com",
		Subject:     "发票附件",
		Body:        "请查收附件",
		Attachments: []string{invoiceFile},
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

	client, err := mailer.New(cfg, zap.NewNop())
	require.NoError(t, err)

	err = client.Send(context.Background(), mailer.Message{
		To:      "buyer@example.com",
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

	client, err := mailer.New(cfg, zap.NewNop())
	require.NoError(t, err)

	err = client.Send(context.Background(), mailer.Message{
		To:      "buyer@example.com",
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

	client, err := mailer.New(cfg, zap.NewNop())
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

func TestUnsupportedDriver(t *testing.T) {
	cfg := config.MailConfig{
		Driver: "sendgrid",
	}

	client, err := mailer.New(cfg, zap.NewNop())
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
}

func TestMemoryMailer(t *testing.T) {
	t.Run("生命周期与发送断言", func(t *testing.T) {
		mem := mailer.NewMemoryMailer()
		require.NotNil(t, mem)
		assert.Equal(t, 0, mem.Count())

		ctx := context.Background()
		msg := mailer.Message{
			To:      "alice@example.com",
			Cc:      []string{"bob@example.com"},
			Subject: "Welcome",
			Body:    "<h1>Hello Alice</h1>",
			IsHTML:  true,
			RawAttachments: []mailer.Attachment{
				mailer.NewAttachmentFromBytes("invoice.pdf", []byte("%PDF-1.4 sample content"), "application/pdf"),
			},
		}

		err := mem.Send(ctx, msg)
		require.NoError(t, err)
		assert.Equal(t, 1, mem.Count())

		mem.AssertSent(t, 1)
		mem.AssertSentTo(t, "alice@example.com")
		mem.AssertSentTo(t, "bob@example.com")

		last, ok := mem.LastMessage()
		assert.True(t, ok)
		assert.Equal(t, "Welcome", last.Subject)
		assert.Len(t, last.RawAttachments, 1)

		// 验证重置
		mem.Reset()
		assert.Equal(t, 0, mem.Count())

		// 验证空收件人校验
		err = mem.Send(ctx, mailer.Message{To: ""})
		require.Error(t, err)

		// 验证关闭后发送报错
		require.NoError(t, mem.Close())
		err = mem.Send(ctx, msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is closed")
	})

	t.Run("通过 New 构造 memory 驱动", func(t *testing.T) {
		client, err := mailer.New(config.MailConfig{Driver: "memory"}, zap.NewNop())
		require.NoError(t, err)
		require.NotNil(t, client)

		err = client.Send(context.Background(), mailer.Message{
			To:      "user@example.com",
			Subject: "Test",
			Body:    "Body",
		})
		require.NoError(t, err)
		require.NoError(t, client.Close())
	})
}

func TestAttachmentAndAllAttachments(t *testing.T) {
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

	t.Run("AllAttachments 合并路径与内存附件", func(t *testing.T) {
		msg := mailer.Message{
			Attachments: []string{tmpFile, ""},
			RawAttachments: []mailer.Attachment{
				mailer.NewAttachmentFromBytes("report.pdf", []byte("pdf content")),
			},
		}

		all := msg.AllAttachments()
		require.Len(t, all, 2)
		assert.Equal(t, "test.txt", all[0].Filename)
		assert.Equal(t, "report.pdf", all[1].Filename)
	})
}

func TestCustomDriverRegistry(t *testing.T) {
	mailer.RegisterDriver("custom-mock", func(_ config.MailConfig, _ *zap.Logger) (mailer.Mailer, error) {
		return mailer.NewMemoryMailer(), nil
	})

	client, err := mailer.New(config.MailConfig{Driver: "custom-mock"}, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NoError(t, client.Close())
}

// TestMail_DisabledFallbackToLog 验证当 MAIL_ENABLED=false 时自动降级为 LogMailer。
func TestMail_DisabledFallbackToLog(t *testing.T) {
	cfg := config.MailConfig{
		Enabled:     new(bool), // *Enabled = false
		Driver:      "mailpit",
		FromAddress: "test@example.com",
	}
	require.False(t, cfg.IsEnabled())
	require.NoError(t, cfg.Validate())

	client, err := mailer.New(cfg, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, client)

	err = client.Send(t.Context(), mailer.Message{
		To:      "user@example.com",
		Subject: "Test Subject",
		Body:    "Hello world",
	})
	require.NoError(t, err)
	require.NoError(t, client.Close())
}

// TestMail_LogAndNullDrivers 验证内置的 log 与 null 驱动正常工作。
func TestMail_LogAndNullDrivers(t *testing.T) {
	for _, driver := range []string{"log", "null"} {
		t.Run(driver, func(t *testing.T) {
			cfg := config.MailConfig{
				Driver: driver,
			}
			require.NoError(t, cfg.Validate())

			client, err := mailer.New(cfg, zap.NewNop())
			require.NoError(t, err)
			require.NotNil(t, client)

			err = client.Send(t.Context(), mailer.Message{
				To:      "user@example.com",
				Subject: "Test Subject",
				Body:    "Hello world",
			})
			require.NoError(t, err)

			err = client.SendMailable(t.Context(), "user@example.com", &testMailable{
				subject: "Template Subject",
				tmpl:    "auth_otp",
				data:    map[string]any{"code": "123456", "ttl_minutes": 10},
			})
			require.NoError(t, err)
			require.NoError(t, client.Close())
		})
	}
}

// TestMailerPingerContract 验证各 Mailer 驱动均实现 Ping、Driver 与 Enabled 探针契约。
func TestMailerPingerContract(t *testing.T) {
	t.Run("log_and_null", func(t *testing.T) {
		logMailer := mailer.NewLogMailer(zap.NewNop())
		require.NoError(t, logMailer.Ping(t.Context()))
		assert.Equal(t, "log", logMailer.Driver())
		assert.True(t, logMailer.Enabled())

		nullMailer := mailer.NewNullMailer()
		require.NoError(t, nullMailer.Ping(t.Context()))
		assert.Equal(t, "null", nullMailer.Driver())
		assert.True(t, nullMailer.Enabled())
	})

	t.Run("memory", func(t *testing.T) {
		memMailer := mailer.NewMemoryMailer()
		require.NoError(t, memMailer.Ping(t.Context()))
		assert.Equal(t, "memory", memMailer.Driver())
		assert.True(t, memMailer.Enabled())
	})

	t.Run("resend", func(t *testing.T) {
		cfg := config.MailConfig{
			Driver: "resend",
			Resend: config.ResendConfig{
				APIKey: "re_test_123",
			},
		}
		client, err := mailer.New(cfg, zap.NewNop())
		require.NoError(t, err)
		require.NoError(t, client.Ping(t.Context()))
		if da, ok := client.(interface{ Driver() string }); ok {
			assert.Equal(t, "resend", da.Driver())
		}
		if ea, ok := client.(interface{ Enabled() bool }); ok {
			assert.True(t, ea.Enabled())
		}
	})
}
