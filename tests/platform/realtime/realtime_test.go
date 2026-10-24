// Package realtime_test 针对 Centrifugo 实时通信基础设施进行全量黑盒契约测试。
package realtime_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/authentication"
	"bgin/internal/platform/realtime"
	"bgin/internal/shared/enums"
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

// TestTokenGenerationAndValidation 验证 Centrifugo JWT 连接令牌的生成与解密有效性。
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

// TestSubscriptionTokenGenerationAndValidation 验证 Centrifugo 频道订阅令牌签发与验签。
func TestSubscriptionTokenGenerationAndValidation(t *testing.T) {
	exp := time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second)

	tok, err := realtime.GenerateSubscriptionToken(
		testSecret,
		"user_202",
		"private:chat.room42",
		exp,
		realtime.WithSubscriptionInfo(map[string]any{"device": "ios"}),
		realtime.WithSubscriptionClient("client-abc"),
	)
	require.NoError(t, err)
	require.NotEmpty(t, tok)

	claims, err := realtime.ValidateSubscriptionToken(testSecret, tok)
	require.NoError(t, err)
	require.NotNil(t, claims)

	assert.Equal(t, "user_202", claims.Subject)
	assert.Equal(t, "private:chat.room42", claims.Channel)
	assert.Equal(t, "client-abc", claims.Client)
	assert.Equal(t, exp.Unix(), claims.ExpiresAt.Unix())
	assert.Equal(t, "ios", claims.Info["device"])

	// 错误密钥验证失败
	_, err = realtime.ValidateSubscriptionToken("invalid-secret", tok)
	require.Error(t, err)

	// 空密钥与空频道失败
	_, err = realtime.GenerateSubscriptionToken("", "user_202", "ch", exp)
	require.Error(t, err)
	_, err = realtime.GenerateSubscriptionToken(testSecret, "user_202", "", exp)
	require.Error(t, err)
}

// TestProtocolGetHeader 验证 ConnectRequest 标头安全不区分大小写提取。
func TestProtocolGetHeader(t *testing.T) {
	req := realtime.ConnectRequest{
		Headers: map[string]any{
			"Authorization": "Bearer token123",
			"X-Custom":      []string{"val1", "val2"},
			"X-Any":         []any{"item1"},
		},
	}

	assert.Equal(t, "Bearer token123", req.GetHeader("authorization"))
	assert.Equal(t, "Bearer token123", req.GetHeader("AUTHORIZATION"))
	assert.Equal(t, "val1", req.GetHeader("x-custom"))
	assert.Equal(t, "item1", req.GetHeader("x-any"))
	assert.Empty(t, req.GetHeader("non-existent"))
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
	realtime.AllowPublic(reg, "news.*")
	err = reg.Authorize(ctx, realtime.User{ID: "1"}, "news.weather")
	require.NoError(t, err)
}

// TestRegistryPublishAndRPC 验证客户端发布策略与 RPC 方法分发。
func TestRegistryPublishAndRPC(t *testing.T) {
	reg := realtime.NewRegistry()

	reg.RegisterPublish("chat.{room}", func(_ context.Context, u realtime.User, _ string, data json.RawMessage, params map[string]string) error {
		if params["room"] == "lobby" && u.ID != "" && len(data) > 0 {
			return nil
		}
		return assert.AnError
	})

	reg.RegisterRPC("echo", func(_ context.Context, u realtime.User, data json.RawMessage) (any, error) {
		return map[string]string{"user": u.ID, "payload": string(data)}, nil
	})

	ctx := context.Background()

	// 发布鉴权
	require.NoError(t, reg.AuthorizePublish(ctx, realtime.User{ID: "u1"}, "chat.lobby", json.RawMessage(`"hello"`)))
	require.Error(t, reg.AuthorizePublish(ctx, realtime.User{ID: "u1"}, "chat.admin", json.RawMessage(`"hello"`)))

	// RPC 分发
	res, err := reg.HandleRPC(ctx, realtime.User{ID: "u1"}, "echo", json.RawMessage(`"hi"`))
	require.NoError(t, err)
	m, ok := res.(map[string]string)
	require.True(t, ok)
	assert.Equal(t, "u1", m["user"])

	// 未知 RPC
	_, err = reg.HandleRPC(ctx, realtime.User{ID: "u1"}, "unknown", nil)
	require.Error(t, err)
}

// TestDSLHelpers 验证声明式 DSL 语法糖。
func TestDSLHelpers(t *testing.T) {
	assert.Equal(t, "private:user:1", realtime.Private("user:%d", 1))
	assert.Equal(t, "private:order", realtime.Private("private:order"))
	assert.Equal(t, "presence:room:10", realtime.Presence("room:%d", 10))
	assert.Equal(t, "presence:room", realtime.Presence("presence:room"))
	assert.Equal(t, "public:news", realtime.Public("public:news"))

	reg := realtime.NewRegistry()

	// 注册 Channel 策略并使用 context 中 identity 认证主体
	realtime.Channel(reg, "orders.{id}", func(_ context.Context, actor realtime.Authenticatable, id string) bool {
		return actor != nil && actor.GetAuthIdentifier() == id
	})

	ctx := authentication.With(context.Background(), authentication.Identity{
		UserID: "100",
		Role:   string(enums.RoleUser),
	})

	err := reg.Authorize(ctx, realtime.User{}, "orders.100")
	require.NoError(t, err)

	err = reg.Authorize(ctx, realtime.User{}, "orders.200")
	require.Error(t, err)

	// ChannelGuard
	realtime.ChannelGuard(reg, "admin.*", "admin")
	require.NoError(t, reg.Authorize(context.Background(), realtime.User{GuardName: "admin"}, "admin.metrics"))
	require.Error(t, reg.Authorize(context.Background(), realtime.User{GuardName: "api"}, "admin.metrics"))

	// AllowUser 高阶助手
	realtime.AllowUser(reg, "private:users.{id}")
	require.NoError(t, reg.Authorize(ctx, realtime.User{}, "private:users.100"))
	require.Error(t, reg.Authorize(ctx, realtime.User{}, "private:users.200"))

	// AllowRole 高阶助手
	realtime.AllowRole(reg, "internal:reports", "admin", "super_admin")
	adminCtx := authentication.With(context.Background(), authentication.Identity{UserID: "admin_1", Role: "admin"})
	userCtx := authentication.With(context.Background(), authentication.Identity{UserID: "user_1", Role: "user"})
	require.NoError(t, reg.Authorize(adminCtx, realtime.User{}, "internal:reports"))
	require.Error(t, reg.Authorize(userCtx, realtime.User{}, "internal:reports"))
}

// TestManagerWithMockServer 针对 Centrifugo HTTP 模拟服务验证 Manager 核心动作。
func TestManagerWithMockServer(t *testing.T) {
	var (
		receivedMethod string
		receivedPath   string
		receivedBody   []byte
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		var buf [2048]byte
		n, _ := r.Body.Read(buf[:])
		receivedBody = buf[:n]

		w.Header().Set("Content-Type", "application/json")
		// 返回标准 Centrifugo 响应格式
		_, _ = w.Write([]byte("{\"result\":{}}\n"))
	}))
	defer server.Close()

	// URL 按文档约定携带完整 API 路径（如 http://127.0.0.1:8000/api），
	// 用带路径的地址才能验证客户端确实把完整地址当作请求基址。
	cfg := &config.CentrifugoConfig{
		Enabled: true,
		URL:     server.URL + "/api",
		APIKey:  "test-api-key",
		Secret:  testSecret,
	}

	mgr := realtime.NewManager(cfg)
	require.NotNil(t, mgr)
	assert.Equal(t, "centrifugo", mgr.GetNodeID())
	assert.NotNil(t, mgr.Registry())
	assert.Equal(t, cfg, mgr.Config())
	assert.NotNil(t, mgr.Client())

	ctx := context.Background()

	// 1. Dispatch
	err := mgr.DispatchContext(ctx, "chat.room1", "message", map[string]string{"text": "hi"})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, receivedMethod)
	assert.NotEmpty(t, receivedBody)

	// 2. Broadcast
	err = mgr.BroadcastContext(ctx, []string{"room1", "room2"}, "alert", "ping")
	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, receivedMethod)

	// 3. PublishRaw / BroadcastRaw
	err = mgr.PublishRaw(ctx, "chat.room1", []byte(`{"raw":true}`))
	require.NoError(t, err)
	err = mgr.BroadcastRaw(ctx, []string{"room1"}, []byte(`{"raw":true}`))
	require.NoError(t, err)

	// 4. Disconnect
	err = mgr.Disconnect(ctx, "user_42")
	require.NoError(t, err)

	// 5. Unsubscribe
	err = mgr.Unsubscribe(ctx, "user_42", "room1")
	require.NoError(t, err)

	// 6. Token
	tok, err := mgr.Token("user_42", time.Now().Add(time.Hour), nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, tok)

	// 7. SubscriptionToken
	subTok, err := mgr.GenerateSubscriptionToken("user_42", "chat.room1", time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.NotEmpty(t, subTok)

	// 8. 验证已禁用 Manager 的行为
	disabledMgr := realtime.NewManager(&config.CentrifugoConfig{Enabled: false})
	require.NoError(t, disabledMgr.DispatchContext(ctx, "channel", "event", nil))
	require.NoError(t, disabledMgr.BroadcastContext(ctx, []string{"ch"}, "event", nil))
	require.NoError(t, disabledMgr.PublishRaw(ctx, "ch", nil))
	require.NoError(t, disabledMgr.BroadcastRaw(ctx, []string{"ch"}, nil))
	require.NoError(t, disabledMgr.Disconnect(ctx, "u"))
	require.NoError(t, disabledMgr.Unsubscribe(ctx, "u", "ch"))

	// 9. Gateway 别名必须与 Manager 同源（统一门面）
	var gw realtime.Gateway = mgr
	require.NotNil(t, gw)

	// 请求必须打到 Centrifugo 的 HTTP API 端点，落到别处即意味着地址拼装有问题。
	assert.Equal(t, "/api", receivedPath)
}

// TestPackageHelperFunctions 验证 package.go 顶层注册工厂。
func TestPackageHelperFunctions(t *testing.T) {
	cfg := &config.Config{
		Realtime: config.CentrifugoConfig{
			Enabled: true,
			URL:     "http://localhost:8000",
			APIKey:  "key",
			Secret:  "secret",
		},
	}

	reg := realtime.NewRegistry()
	gw, err := realtime.RegisterRealtimeGateway(cfg, reg)
	require.NoError(t, err)
	require.NotNil(t, gw)
	assert.Equal(t, reg, gw.Registry())

	mgr, err := realtime.RegisterRealtime(cfg)
	require.NoError(t, err)
	require.NotNil(t, mgr)

	// 未启用时 RegisterRealtime 报错
	disabledCfg := &config.Config{}
	_, err = realtime.RegisterRealtime(disabledCfg)
	require.ErrorIs(t, err, realtime.ErrRealtimeDisabled)
}
