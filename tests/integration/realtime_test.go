//go:build integration

// Package integration_test 针对真实运行的 Centrifugo 实时通信节点与 Gin 后端端点进行全量真实链路集成测试。
// 运行方式：rtk go test -v -tags=integration -run TestRealtime ./tests/integration/...
package integration_test

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"bgin/internal/config"
	featureRealtime "bgin/internal/features/realtime"
	platformRealtime "bgin/internal/platform/realtime"
)

// getRealtimeConfig 加载当前环境的 Centrifugo 配置。
func getRealtimeConfig(t *testing.T) config.CentrifugoConfig {
	t.Helper()

	var rtCfg config.CentrifugoConfig
	if cfg, err := config.Load(); err == nil {
		rtCfg = cfg.Realtime
	}

	// 允许通过环境变量快速覆盖
	if u := os.Getenv("CENTRIFUGO_URL"); u != "" {
		rtCfg.URL = u
		rtCfg.Enabled = true
	}
	if k := os.Getenv("CENTRIFUGO_API_KEY"); k != "" {
		rtCfg.APIKey = k
	}
	if s := os.Getenv("CENTRIFUGO_SECRET"); s != "" {
		rtCfg.Secret = s
	}

	// 默认本地开发端口回退
	if rtCfg.URL == "" {
		rtCfg.URL = "http://127.0.0.1:48000/api"
	}
	// 凭据一律来自环境变量或 .env，测试代码内不保留任何默认值：
	// 缺失即说明本地未配置实时通信环境，安全跳过而不是拿占位凭据去连。
	if rtCfg.APIKey == "" || rtCfg.Secret == "" {
		t.Skip("CENTRIFUGO_API_KEY / CENTRIFUGO_SECRET are not configured, skipping live realtime integration test")
	}
	rtCfg.Enabled = true

	return rtCfg
}

// ensureCentrifugoReachable 探测 Centrifugo 服务是否存活，不可达时安全跳过。
func ensureCentrifugoReachable(t *testing.T, cfg config.CentrifugoConfig) {
	t.Helper()

	parsed, err := url.Parse(cfg.URL)
	require.NoError(t, err)

	healthURL := fmt.Sprintf("%s://%s/health", parsed.Scheme, parsed.Host)
	client := &http.Client{Timeout: 1 * time.Second}

	resp, err := client.Get(healthURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Skipf("Centrifugo 服务未就绪 (%s: %v)，跳过实时通信真实集成测试", healthURL, err)
	}
	_ = resp.Body.Close()
}

// TestRealtime_CentrifugoHealthAndInfo 真实探测 Centrifugo 节点健康状态与集群元信息。
func TestRealtime_CentrifugoHealthAndInfo(t *testing.T) {
	cfg := getRealtimeConfig(t)
	ensureCentrifugoReachable(t, cfg)

	ctx := t.Context()
	mgr := platformRealtime.NewManager(&cfg)
	require.NotNil(t, mgr)
	require.NotNil(t, mgr.Client())

	info, err := mgr.Client().Info(ctx)
	require.NoError(t, err, "调用 Centrifugo HTTP API Info 失败")
	require.NotEmpty(t, info.Nodes, "应当返回至少一个 Centrifugo 节点信息")

	node := info.Nodes[0]
	assert.NotEmpty(t, node.UID)
	assert.NotEmpty(t, node.Version)
	assert.Equal(t, "centrifugo", mgr.GetNodeID())
}

// TestRealtime_PublishBroadcastAndHistory 真实验证 HTTP API 发布、批量广播与历史消息回溯。
func TestRealtime_PublishBroadcastAndHistory(t *testing.T) {
	cfg := getRealtimeConfig(t)
	ensureCentrifugoReachable(t, cfg)

	ctx := t.Context()
	mgr := platformRealtime.NewManager(&cfg)

	// 使用 public 命名空间下的动态隔离频道（具有 history 存储与客户端读取权限）
	channelA := fmt.Sprintf("public:test-room-%d", time.Now().UnixNano())
	channelB := fmt.Sprintf("public:test-room-%d-alt", time.Now().UnixNano())

	// 1. 测试单频道 Dispatch
	eventA := "message.new"
	dataA := map[string]any{
		"text":      "hello real world centrifugo",
		"timestamp": time.Now().Unix(),
	}
	err := mgr.DispatchContext(ctx, channelA, eventA, dataA)
	require.NoError(t, err, "向单频道推送消息失败")

	// 2. 测试批量广播 Broadcast
	eventB := "room.broadcast"
	dataB := map[string]any{"announcement": "server update ready"}
	err = mgr.BroadcastContext(ctx, []string{channelA, channelB}, eventB, dataB)
	require.NoError(t, err, "向多频道批量广播失败")

	// 3. 测试 History 历史查询回溯刚刚发布的消息。
	// 历史入库是异步的：轮询到条件成立为止，避免慢环境下的固定时长盲等造成偶发失败。
	require.Eventually(t, func() bool {
		pubs, historyErr := mgr.History(ctx, channelA, 10)
		if historyErr != nil || len(pubs) < 2 {
			return false
		}
		for _, pub := range pubs {
			if strings.Contains(string(pub.Data), "hello real world centrifugo") {
				return true
			}
		}
		return false
	}, 5*time.Second, 100*time.Millisecond, "历史记录中应能回溯到刚推送的实时内容")

	// 4. 测试 PresenceStats 在线统计查询
	stats, err := mgr.PresenceStats(ctx, channelA)
	require.NoError(t, err, "查询频道在线统计失败")
	assert.GreaterOrEqual(t, int(stats.NumClients), 0, "客户端在线数应大于等于 0")
}

// TestRealtime_SSEClientStreamingVerification 真实验证客户端经 SSE 建立长连接并实时接收服务端下发事件。
func TestRealtime_SSEClientStreamingVerification(t *testing.T) {
	cfg := getRealtimeConfig(t)
	ensureCentrifugoReachable(t, cfg)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	parsed, err := url.Parse(cfg.URL)
	require.NoError(t, err)

	// 为测试用户签发连接凭证，预订阅该动态隔离频道
	channel := fmt.Sprintf("public:sse-live-%d", time.Now().UnixNano())
	userID := fmt.Sprintf("user_sse_%d", time.Now().UnixNano())
	expireAt := time.Now().Add(10 * time.Minute)

	token, err := platformRealtime.GenerateConnectionToken(
		cfg.Secret,
		userID,
		expireAt,
		map[string]any{"source": "integration_test"},
		nil,
		channel,
	)
	require.NoError(t, err, "签发客户端连接 Token 失败")

	// 构造 Centrifugo unidirectional SSE 连接地址 (/connection/uni_sse)
	connectCmd, err := json.Marshal(map[string]any{"token": token})
	require.NoError(t, err)

	sseURL := fmt.Sprintf("%s://%s/connection/uni_sse?cf_connect=%s",
		parsed.Scheme,
		parsed.Host,
		url.QueryEscape(string(connectCmd)),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sseURL, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")

	// 发起长连接
	sseClient := &http.Client{Timeout: 0}
	resp, err := sseClient.Do(req)
	require.NoError(t, err, "建立 SSE 实时长连接失败")
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode, "SSE 握手应返回 200 OK")
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

	// 后台接收 SSE 推送的数据
	receivedMessages := make(chan string, 10)
	readerErr := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if payload, ok := strings.CutPrefix(line, "data: "); ok {
				select {
				case receivedMessages <- payload:
				default:
				}
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			readerErr <- scanErr
		}
	}()

	// 等客户端把订阅建起来：SSE 协议没有可供客户端观测的订阅确认帧，
	// 这里只能给出一个明确的短等待窗口，其充分性由下方 4 秒的接收超时兜底并给出可诊断的失败。
	time.Sleep(150 * time.Millisecond)

	// 服务端派发实时事件
	mgr := platformRealtime.NewManager(&cfg)
	expectedMsg := fmt.Sprintf("unique-token-%d", time.Now().UnixNano())
	dispatchErr := mgr.DispatchContext(ctx, channel, "chat.realtime", map[string]any{
		"greeting": "welcome to real-time centrifugo",
		"token":    expectedMsg,
	})
	require.NoError(t, dispatchErr, "服务端派发实时消息失败")

	// 等待客户端接收到该推送
	var matchedPayload string
	deadline := time.After(4 * time.Second)
	found := false

	for !found {
		select {
		case msg := <-receivedMessages:
			if strings.Contains(msg, expectedMsg) {
				matchedPayload = msg
				found = true
			}
		case err := <-readerErr:
			t.Fatalf("SSE 流读取异常: %v", err)
		case <-deadline:
			t.Fatal("超时未在 SSE 长连接流中接收到服务端实时派发的消息")
		}
	}

	assert.NotEmpty(t, matchedPayload, "客户端应能接收并读取到实时推送事件")
	assert.Contains(t, matchedPayload, "chat.realtime")
	assert.Contains(t, matchedPayload, expectedMsg)
}

// TestRealtime_ProxySubscribeCallbackContract 真实验证 Centrifugo Proxy 订阅鉴权回调链路。
func TestRealtime_ProxySubscribeCallbackContract(t *testing.T) {
	cfg := getRealtimeConfig(t)

	// 构造 Centrifugo Proxy 订阅请求体
	reqPayload := platformRealtime.SubscribeRequest{
		Client:    "client-session-123456",
		Transport: "websocket",
		Protocol:  "json",
		Encoding:  "json",
		User:      "usr_admin_001",
		Channel:   "public:lounge",
		Data:      json.RawMessage(`{"source":"test"}`),
	}

	rawBody, err := json.Marshal(reqPayload)
	require.NoError(t, err)

	// 按照 HMAC-SHA256 计算签名
	mac := hmac.New(sha256.New, []byte(cfg.Secret))
	mac.Write(rawBody)
	signature := hex.EncodeToString(mac.Sum(nil))

	// 构建一个标准的 Router 引擎（直接注入业务路由验证）
	reg := platformRealtime.NewRegistry()
	reg.Register("public:lounge", func(_ context.Context, _ platformRealtime.User, _ string, _ map[string]string) error {
		return nil
	})

	appCfg := &config.Config{Realtime: cfg}
	gw := platformRealtime.NewManager(&cfg, platformRealtime.WithRegistry(reg))
	svc := featureRealtime.NewService(featureRealtime.Deps{
		Gateway: gw,
		Cfg:     appCfg,
		Secret:  appCfg.Realtime.Secret,
	})
	handler := featureRealtime.NewHandler(svc)

	router := gin.New()
	router.POST("/v1/realtime/proxy/subscribe", handler.ProxySubscribe)

	// 1. 合法请求与有效签名
	w := httptest.NewRecorder()
	httpReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/realtime/proxy/subscribe", strings.NewReader(string(rawBody)))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Centrifugo-Signature", signature)

	router.ServeHTTP(w, httpReq)
	assert.Equal(t, http.StatusOK, w.Code)

	var respBody map[string]any
	err = json.Unmarshal(w.Body.Bytes(), &respBody)
	require.NoError(t, err)
	assert.NotNil(t, respBody["result"], "合法的订阅请求应返回 result 字段以准许订阅")

	// 2. 伪造/非法签名应返回 403 Forbidden
	wBad := httptest.NewRecorder()
	badReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/realtime/proxy/subscribe", strings.NewReader(string(rawBody)))
	require.NoError(t, err)
	badReq.Header.Set("Content-Type", "application/json")
	badReq.Header.Set("X-Centrifugo-Signature", "tampered-bad-signature")

	router.ServeHTTP(wBad, badReq)
	assert.Equal(t, http.StatusForbidden, wBad.Code)
}

// TestRealtime_WebSocketClientStreamingVerification 真实验证客户端经 WebSocket 握手并实时接收推送事件。
func TestRealtime_WebSocketClientStreamingVerification(t *testing.T) {
	cfg := getRealtimeConfig(t)
	ensureCentrifugoReachable(t, cfg)

	parsed, err := url.Parse(cfg.URL)
	require.NoError(t, err)

	wsScheme := "ws"
	if parsed.Scheme == "https" {
		wsScheme = "wss"
	}
	wsURL := fmt.Sprintf("%s://%s/connection/websocket", wsScheme, parsed.Host)

	// 尝试常见合法 Origin 握手连接
	origins := []string{
		fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host),
		"http://127.0.0.1:48000",
		"http://localhost:48000",
		"http://127.0.0.1",
		"http://localhost",
	}

	var wsConn *websocket.Conn
	var dialErr error
	for _, o := range origins {
		wsConn, dialErr = websocket.Dial(wsURL, "", o)
		if dialErr == nil {
			break
		}
	}
	if wsConn == nil {
		t.Skipf("当前 Centrifugo 节点环境配置限制了客户端 WebSocket Origin 握手 (%v)，跳过 WebSocket 客户端直连用例", dialErr)
		return
	}
	defer func() { _ = wsConn.Close() }()

	channel := fmt.Sprintf("public:ws-live-%d", time.Now().UnixNano())
	userID := fmt.Sprintf("user_ws_%d", time.Now().UnixNano())
	token, err := platformRealtime.GenerateConnectionToken(
		cfg.Secret,
		userID,
		time.Now().Add(10*time.Minute),
		nil,
		nil,
	)
	require.NoError(t, err)

	// 1. 发送 Centrifugo Client 协议 Connect 命令
	connectCmd := fmt.Sprintf(`{"id":1,"connect":{"token":"%s"}}`, token)
	_, err = wsConn.Write([]byte(connectCmd))
	require.NoError(t, err)

	var connectResp [2048]byte
	n, err := wsConn.Read(connectResp[:])
	require.NoError(t, err)
	assert.Contains(t, string(connectResp[:n]), `"connect"`)

	// 2. 发送 Subscribe 命令
	subCmd := fmt.Sprintf(`{"id":2,"subscribe":{"channel":"%s"}}`, channel)
	_, err = wsConn.Write([]byte(subCmd))
	require.NoError(t, err)

	var subResp [2048]byte
	n, err = wsConn.Read(subResp[:])
	require.NoError(t, err)
	assert.Contains(t, string(subResp[:n]), `"subscribe"`)

	// 3. 服务端派发实时事件
	mgr := platformRealtime.NewManager(&cfg)
	expectedWSMsg := fmt.Sprintf("ws-payload-%d", time.Now().UnixNano())
	err = mgr.DispatchContext(t.Context(), channel, "chat.ws_event", map[string]any{
		"token": expectedWSMsg,
	})
	require.NoError(t, err)

	// 4. WebSocket 客户端接收推送帧
	_ = wsConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var pushResp [4096]byte
	n, err = wsConn.Read(pushResp[:])
	require.NoError(t, err, "读取 WebSocket 推送帧失败")
	assert.Contains(t, string(pushResp[:n]), expectedWSMsg)
	assert.Contains(t, string(pushResp[:n]), "chat.ws_event")
}
