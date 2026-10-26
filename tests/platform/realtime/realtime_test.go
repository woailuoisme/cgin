// Package realtime_test 针对 Centrifugo 实时通信基础设施进行黑盒契约测试。
package realtime_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/pkg/realtime"
)

const testSecret = "centrifugo-test-secret-key-12345"

// TestCryptoVerifySignature 验证 Centrifugo Proxy 请求 HMAC-SHA256 签名校验。
func TestCryptoVerifySignature(t *testing.T) {
	body := []byte(`{"client":"123","user":"456"}`)

	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write(body)
	validSig := hex.EncodeToString(mac.Sum(nil))

	assert.True(t, realtime.VerifySignature(testSecret, body, validSig))
	assert.False(t, realtime.VerifySignature(testSecret, body, "invalid-sig"))
	assert.False(t, realtime.VerifySignature(testSecret, body, ""))
	assert.False(t, realtime.VerifySignature("", body, "any-sig"), "空密钥必须 fail-closed，不得放行")
}

// TestTokenGenerationAndValidation 验证 Centrifugo JWT 连接令牌的签发与校验。
func TestTokenGenerationAndValidation(t *testing.T) {
	exp := time.Now().UTC().Add(1 * time.Hour).Truncate(time.Second)
	info := map[string]any{"name": "Alice"}
	meta := map[string]any{"role": "admin"}

	tok, err := realtime.GenerateConnectionToken(testSecret, "user_101", exp, info, meta, "orders", "news")
	require.NoError(t, err)
	require.NotEmpty(t, tok)

	claims, err := realtime.ValidateConnectionToken(testSecret, tok)
	require.NoError(t, err)
	require.NotNil(t, claims)

	assert.Equal(t, "user_101", claims.Subject)
	assert.Equal(t, exp.Unix(), claims.ExpiresAt.Unix())
	assert.Equal(t, []string{"orders", "news"}, claims.Channels)
	assert.Equal(t, "Alice", claims.Info["name"])
	assert.Equal(t, "admin", claims.Meta["role"])

	// 错误密钥验签失败
	_, err = realtime.ValidateConnectionToken("wrong-secret", tok)
	require.Error(t, err)

	// 空密钥报错
	_, err = realtime.GenerateConnectionToken("", "user_101", exp, nil, nil)
	require.Error(t, err)
	_, err = realtime.ValidateConnectionToken("", tok)
	require.Error(t, err)
}

// TestRegistryChannelAuthorization 验证频道路由、动态通配符与订阅授权策略。
func TestRegistryChannelAuthorization(t *testing.T) {
	reg := realtime.NewRegistry()

	// 静态精确匹配
	reg.Register("public:chat", func(_ context.Context, _ realtime.User, _ string, _ map[string]string) error {
		return nil
	})

	// 动态参数匹配
	reg.Register("private:user.{id}", func(_ context.Context, u realtime.User, _ string, params map[string]string) error {
		if params["id"] == u.ID {
			return nil
		}
		return assert.AnError
	})

	ctx := context.Background()

	// 静态匹配成功
	err := reg.Authorize(ctx, realtime.User{ID: "1"}, "public:chat")
	require.NoError(t, err)

	// 动态匹配成功
	err = reg.Authorize(ctx, realtime.User{ID: "42"}, "private:user.42")
	require.NoError(t, err)

	// 动态匹配失败
	err = reg.Authorize(ctx, realtime.User{ID: "99"}, "private:user.42")
	require.Error(t, err)

	// 未注册的私有频道必须遵循 Fail-Closed 阻断
	err = reg.Authorize(ctx, realtime.User{ID: "1"}, "private:secret_room")
	require.Error(t, err)

	// 未白名单注册的普通频道同样遵循全局严格 Fail-Closed 阻断
	err = reg.Authorize(ctx, realtime.User{ID: "1"}, "news.weather")
	require.Error(t, err)

	// 显式白名单注册的公开广播频道允许订阅
	reg.Register("news.*", func(_ context.Context, _ realtime.User, _ string, _ map[string]string) error {
		return nil
	})
	err = reg.Authorize(ctx, realtime.User{ID: "1"}, "news.weather")
	require.NoError(t, err)
}

// publishCommand 是 Centrifugo HTTP API 单条管道命令（NDJSON，一行一条）的最小投影。
type publishCommand struct {
	Method string `json:"method"`
	Params struct {
		Channel        string          `json:"channel"`
		Data           json.RawMessage `json:"data"`
		IdempotencyKey string          `json:"idempotency_key"`
	} `json:"params"`
}

// TestManagerPublish 针对 Centrifugo HTTP 模拟服务验证发布动作与幂等键。
func TestManagerPublish(t *testing.T) {
	var (
		receivedMethod string
		receivedPath   string
		receivedBody   []byte
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		receivedBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"result\":{}}\n"))
	}))
	defer server.Close()

	cfg := &config.CentrifugoConfig{
		Enabled: true,
		URL:     server.URL + "/api",
		APIKey:  "test-api-key",
		Secret:  testSecret,
	}

	gw := realtime.NewManager(cfg, nil)
	require.NotNil(t, gw.Registry())
	assert.True(t, gw.Enabled())
	assert.Equal(t, "centrifugo", gw.Driver())

	// 1. 发布必须打到 Centrifugo HTTP API 端点
	err := gw.DispatchContext(t.Context(), "chat.room1", "message", map[string]string{"text": "hi"})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, receivedMethod)
	assert.Equal(t, "/api", receivedPath)

	// 2. 必须携带幂等键，否则重试会让订阅端收到重复事件
	var cmd publishCommand
	firstLine, _, _ := bytes.Cut(receivedBody, []byte("\n"))
	require.NoError(t, json.Unmarshal(firstLine, &cmd))
	assert.Equal(t, "publish", cmd.Method)
	assert.Equal(t, "chat.room1", cmd.Params.Channel)
	assert.NotEmpty(t, cmd.Params.IdempotencyKey)
	assert.JSONEq(t, `{"event":"message","data":{"text":"hi"}}`, string(cmd.Params.Data))

	// 3. 空频道或空事件属于调用方错误
	require.Error(t, gw.DispatchContext(t.Context(), "", "message", nil))
	require.Error(t, gw.DispatchContext(t.Context(), "chat.room1", "", nil))

	// 4. 连通性探针
	require.NoError(t, gw.Ping(t.Context()))
}

// TestManagerDisabled 验证未启用时的降级行为：发布静默跳过、探针显式报错。
func TestManagerDisabled(t *testing.T) {
	gw := realtime.NewManager(&config.CentrifugoConfig{Enabled: false}, nil)

	assert.False(t, gw.Enabled())
	require.NoError(t, gw.DispatchContext(t.Context(), "ch", "event", nil))
	require.Error(t, gw.Ping(t.Context()))

	// 未配置 Secret 时无法签发凭据
	_, err := gw.GenerateConnectionToken("u1", time.Now().Add(time.Hour), nil, nil)
	require.Error(t, err)
}

// TestNewManagerNilConfig 验证零配置构造不得 panic。
func TestNewManagerNilConfig(t *testing.T) {
	gw := realtime.NewManager(nil, nil)

	require.NotNil(t, gw)
	assert.False(t, gw.Enabled())
	require.NoError(t, gw.DispatchContext(t.Context(), "ch", "event", nil))
	assert.NotNil(t, gw.Registry())
}
