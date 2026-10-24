package eventbus_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/goleak"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/eventbus"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type userRegisteredEvent struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

func (e userRegisteredEvent) EventName() string {
	return "user.registered"
}

type plainEvent struct {
	Message string `json:"message"`
}

// TestEventBus_NewAndRegister 验证事件总线的构造、配置默认值与非法驱动校验。
func TestEventBus_NewAndRegister(t *testing.T) {
	log := zap.NewNop()

	// 1. 默认注册
	mgr, err := eventbus.RegisterEventBus(nil, log)
	require.NoError(t, err)
	require.NotNil(t, mgr)
	require.NotNil(t, mgr.EventBus())
	require.NotNil(t, mgr.EventProcessor())
	require.NotNil(t, mgr.Router())
	require.NotNil(t, mgr.Publisher())
	require.NotNil(t, mgr.Subscriber())
	require.NoError(t, mgr.Close())

	// 2. 显式配置注册
	cfg := &config.Config{
		EventBus: config.EventBusConfig{
			Driver:      eventbus.DriverGoChannel,
			TopicPrefix: "app_test",
			LogLevel:    "debug",
			BufferSize:  512,
		},
	}
	mgr2, err := eventbus.RegisterEventBus(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, mgr2)
	require.NoError(t, mgr2.Close())

	// 3. 非法驱动返回错误
	invalidCfg := config.EventBusConfig{
		Driver: "unsupported_kafka",
	}
	_, err = eventbus.NewManager(invalidCfg, log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported event bus driver")
}

// TestEventBus_PublishAndListen 验证强类型领域事件的发布、监听、消费与泛型解析链路。
func TestEventBus_PublishAndListen(t *testing.T) {
	mgr := eventbus.NewTestManager()
	defer func() {
		_ = mgr.Close()
	}()

	var receivedUser userRegisteredEvent
	var userMu sync.Mutex
	userDone := make(chan struct{})

	// 1. 通过泛型 Listen 注册监听器
	err := eventbus.Listen(mgr, "handle_user_registered", func(ctx context.Context, ev *userRegisteredEvent) error {
		userMu.Lock()
		defer userMu.Unlock()
		receivedUser = *ev
		close(userDone)
		return nil
	})
	require.NoError(t, err)

	var receivedPlain plainEvent
	var plainMu sync.Mutex
	plainDone := make(chan struct{})

	// 2. 通过通用 Handler 注册没有显式实现 Event 契约的普通结构体
	plainHandler := cqrs.NewEventHandler("handle_plain_event", func(ctx context.Context, ev *plainEvent) error {
		plainMu.Lock()
		defer plainMu.Unlock()
		receivedPlain = *ev
		close(plainDone)
		return nil
	})
	err = mgr.RegisterHandlers(plainHandler)
	require.NoError(t, err)

	// 3. 异步启动事件路由器
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		_ = mgr.Run(ctx)
	}()

	// 等待路由器运行就绪
	select {
	case <-mgr.Running():
	case <-time.After(3 * time.Second):
		t.Fatal("router failed to start in time")
	}

	// 4. 发布事件
	testEv := userRegisteredEvent{
		UserID: "u-999",
		Email:  "test@example.com",
	}
	err = mgr.Publish(ctx, testEv)
	require.NoError(t, err)

	plainEv := plainEvent{Message: "hello world"}
	err = mgr.Publish(ctx, plainEv)
	require.NoError(t, err)

	// 5. 校验消费成功
	select {
	case <-userDone:
		userMu.Lock()
		assert.Equal(t, "u-999", receivedUser.UserID)
		assert.Equal(t, "test@example.com", receivedUser.Email)
		userMu.Unlock()
	case <-time.After(3 * time.Second):
		t.Fatal("user event was not handled in time")
	}

	select {
	case <-plainDone:
		plainMu.Lock()
		assert.Equal(t, "hello world", receivedPlain.Message)
		plainMu.Unlock()
	case <-time.After(3 * time.Second):
		t.Fatal("plain event was not handled in time")
	}
}

// TestEventBus_MetadataInjection 验证 OpenTelemetry TraceID 与 CorrelationID 的双向透传与上下文还原。
func TestEventBus_MetadataInjection(t *testing.T) {
	mgr := eventbus.NewTestManager()
	defer func() {
		_ = mgr.Close()
	}()

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)

	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})

	publishCtx := trace.ContextWithSpanContext(t.Context(), spanCtx)
	publishCtx = eventbus.ContextWithRequestID(publishCtx, "req-12345")

	var consumerTraceID string
	var consumerReqID string
	var mu sync.Mutex
	done := make(chan struct{})

	// 注册监听器并在消费端检验 Context 自动还原的 TraceID 与 RequestID
	err = eventbus.Listen(mgr, "verify_metadata_propagation", func(ctx context.Context, ev *userRegisteredEvent) error {
		mu.Lock()
		defer mu.Unlock()
		if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
			consumerTraceID = sc.TraceID().String()
		}
		if reqID := eventbus.RequestIDFromContext(ctx); reqID != "" {
			consumerReqID = reqID
		}
		close(done)
		return nil
	})
	require.NoError(t, err)

	// 启动路由器
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_ = mgr.Run(ctx)
	}()

	select {
	case <-mgr.Running():
	case <-time.After(3 * time.Second):
		t.Fatal("router failed to start in time")
	}

	require.NoError(t, mgr.Publish(publishCtx, userRegisteredEvent{UserID: "u-meta", Email: "meta@example.com"}))

	select {
	case <-done:
		mu.Lock()
		assert.Equal(t, spanCtx.TraceID().String(), consumerTraceID, "消费端 Context 应完整还原 TraceID")
		assert.Equal(t, "req-12345", consumerReqID, "消费端 Context 应完整还原 RequestID")
		mu.Unlock()
	case <-time.After(3 * time.Second):
		t.Fatal("event was not delivered in time")
	}
}

// TestEventBus_LifecycleAndErrors 验证生命周期边界与错误分支。
func TestEventBus_LifecycleAndErrors(t *testing.T) {
	mgr := eventbus.NewTestManager()

	// 1. 批量/空处理器注册
	err := mgr.RegisterHandler()
	require.NoError(t, err)

	err = mgr.RegisterHandler(nil)
	require.NoError(t, err)

	// 2. 正常运行与重复运行检测
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		_ = mgr.Run(ctx)
	}()

	<-mgr.Running()

	err = mgr.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already running")

	// 3. 优雅停止与幂等关闭
	require.NoError(t, mgr.Shutdown())
	require.NoError(t, mgr.Close())

	// 4. 未初始化对象调用安全
	var nilMgr *eventbus.Manager
	require.Error(t, nilMgr.Publish(context.Background(), plainEvent{}))
	require.Error(t, nilMgr.RegisterHandlers())
	require.Error(t, nilMgr.RegisterHandler(nil))
	require.NotNil(t, nilMgr.Running())
	<-nilMgr.Running()
}

// TestEventBus_DriverRegistry 验证传输后端驱动工厂注册机制 (OCP)。
func TestEventBus_DriverRegistry(t *testing.T) {
	customDriverCalled := false
	eventbus.RegisterDriver("custom_mock", func(cfg config.EventBusConfig, logger watermill.LoggerAdapter) (eventbus.PubSubDriver, error) {
		customDriverCalled = true
		return gochannel.NewGoChannel(gochannel.Config{}, logger), nil
	})

	cfg := config.EventBusConfig{
		Driver:      "custom_mock",
		TopicPrefix: "custom",
	}

	mgr, err := eventbus.NewManager(cfg, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, mgr)
	assert.True(t, customDriverCalled)
	_ = mgr.Close()
}

// TestEventBus_FunctionalOptions 验证函数式配置选项。
func TestEventBus_FunctionalOptions(t *testing.T) {
	middlewareCalled := false
	customMW := func(h message.HandlerFunc) message.HandlerFunc {
		return func(msg *message.Message) ([]*message.Message, error) {
			middlewareCalled = true
			return h(msg)
		}
	}

	mgr := eventbus.NewTestManager(
		eventbus.WithCloseTimeout(2*time.Second),
		eventbus.WithRetry(2, 50*time.Millisecond, 200*time.Millisecond),
		eventbus.WithHandlerTimeout(5*time.Second),
		eventbus.WithChannelBuffer(512),
		eventbus.WithMiddleware(customMW),
	)
	defer func() {
		_ = mgr.Close()
	}()

	done := make(chan struct{})
	err := eventbus.Listen(mgr, "option_test_handler", func(ctx context.Context, ev *plainEvent) error {
		close(done)
		return nil
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_ = mgr.Run(ctx)
	}()

	<-mgr.Running()

	require.NoError(t, mgr.Publish(ctx, plainEvent{Message: "test options"}))

	select {
	case <-done:
		assert.True(t, middlewareCalled, "自定义中间件应被执行")
	case <-time.After(3 * time.Second):
		t.Fatal("event not handled")
	}
}

// TestZapLoggerAdapter 验证 Zap 适配器的日志记录与字段拷贝能力。
func TestZapLoggerAdapter(t *testing.T) {
	adapter := eventbus.NewZapLoggerAdapter(zap.NewNop())

	adapter.Info("test info", watermill.LogFields{"key": "val"})
	adapter.Debug("test debug", nil)
	adapter.Trace("test trace", nil)
	adapter.Error("test error", errors.New("sample error"), watermill.LogFields{"err_key": 1})

	// With 字段扩展
	withEmpty := adapter.With(nil)
	assert.Equal(t, adapter, withEmpty)

	withFields := adapter.With(watermill.LogFields{"scope": "bus"})
	assert.NotNil(t, withFields)
}
