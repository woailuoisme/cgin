package eventbus_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/goleak"

	"cgin/internal/config"
	"cgin/pkg/errs"
	"cgin/pkg/eventbus"
	"cgin/pkg/logger"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type userRegisteredEvent struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

func (userRegisteredEvent) EventName() string {
	return "user.registered"
}

type orderPaidEvent struct {
	OrderNo string `json:"orderNo"`
}

func (orderPaidEvent) EventName() string {
	return "order.paid"
}

// TestEventBus_NewAndDriverValidation 验证事件总线的构造、配置默认值、NopPublisher 与非法驱动校验。
func TestEventBus_NewAndDriverValidation(t *testing.T) {
	log := logger.Nop()

	// 1. 显式配置构造
	mgr, err := eventbus.NewManager(config.EventBusConfig{
		Driver:      eventbus.DriverGoChannel,
		TopicPrefix: "app_test",
		LogLevel:    "debug",
		BufferSize:  512,
	}, log)
	require.NoError(t, err)
	require.NotNil(t, mgr)
	require.NoError(t, mgr.Close())

	// 2. 空对象 NopPublisher 零副作用
	nop := eventbus.NopPublisher()
	require.NotNil(t, nop)
	require.NoError(t, nop.Publish(context.Background(), userRegisteredEvent{UserID: "u-1"}))

	// 3. 非法驱动立即返回错误
	invalidCfg := config.EventBusConfig{
		Driver: "unsupported_kafka",
	}
	_, err = eventbus.NewManager(invalidCfg, log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported event bus driver")
}

// TestEventBus_PublishAndListen 验证强类型领域事件的发布、监听与消费闭环。
func TestEventBus_PublishAndListen(t *testing.T) {
	mgr := eventbus.NewTestManager()
	defer func() {
		_ = mgr.Close()
	}()

	var receivedUser userRegisteredEvent
	var userMu sync.Mutex
	userDone := make(chan struct{})

	err := eventbus.Listen(mgr, "handle_user_registered", func(_ context.Context, ev *userRegisteredEvent) error {
		userMu.Lock()
		defer userMu.Unlock()
		receivedUser = *ev
		close(userDone)
		return nil
	})
	require.NoError(t, err)

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

	testEv := userRegisteredEvent{
		UserID: "u-999",
		Email:  "test@example.com",
	}
	require.NoError(t, mgr.Publish(ctx, testEv))

	select {
	case <-userDone:
		userMu.Lock()
		assert.Equal(t, "u-999", receivedUser.UserID)
		assert.Equal(t, "test@example.com", receivedUser.Email)
		userMu.Unlock()
	case <-time.After(3 * time.Second):
		t.Fatal("user event was not handled in time")
	}
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestEventBus_ObservabilityContextPropagation 验证 pkg/logger request_id 与 OpenTelemetry SpanContext 的端到端透传。
func TestEventBus_ObservabilityContextPropagation(t *testing.T) {
	var logBuf safeBuffer
	appLog := logger.NewWithOptions(
		logger.WithChannel("json"),
		logger.WithLevel("info"),
		logger.WithWriter(&logBuf),
		logger.WithRedaction(false),
	)

	mgr, err := eventbus.NewManager(config.EventBusConfig{
		Driver:      eventbus.DriverGoChannel,
		TopicPrefix: "obs",
		LogLevel:    "error",
	}, appLog)
	require.NoError(t, err)
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
	publishCtx = logger.WithContext(publishCtx, appLog, "req-12345")

	var consumerTraceID string
	var consumerReqID string
	var mu sync.Mutex
	done := make(chan struct{})

	err = eventbus.Listen(mgr, "verify_metadata_propagation", func(ctx context.Context, ev *userRegisteredEvent) error {
		mu.Lock()
		defer mu.Unlock()
		if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
			consumerTraceID = sc.TraceID().String()
		}
		consumerReqID = logger.RequestIDFromContext(ctx)
		logger.FromContext(ctx).Info().Str("user_id", ev.UserID).Msg("consumer logged event")
		close(done)
		return nil
	})
	require.NoError(t, err)

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
		assert.Equal(t, "req-12345", consumerReqID, "消费端 Context 应通过 logger.RequestIDFromContext 完整还原 request_id")
		assert.Contains(t, logBuf.String(), `"request_id":"req-12345"`, "消费端 logger.FromContext(ctx) 应自动携带 request_id")
		assert.Contains(t, logBuf.String(), `"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"`, "消费端 logger.FromContext(ctx) 应自动携带 trace_id")
		mu.Unlock()
	case <-time.After(3 * time.Second):
		t.Fatal("event was not delivered in time")
	}
}

// TestEventBus_RetryAndFinalErrorBoundary 验证处理器失败时按配置重试，耗尽后由错误边界记录日志并终止无限 Nack 重投。
func TestEventBus_RetryAndFinalErrorBoundary(t *testing.T) {
	var logBuf safeBuffer
	appLog := logger.NewWithOptions(
		logger.WithChannel("json"),
		logger.WithLevel("error"),
		logger.WithWriter(&logBuf),
		logger.WithRedaction(false),
	)

	mgr, err := eventbus.NewManager(
		config.EventBusConfig{
			Driver:      eventbus.DriverGoChannel,
			TopicPrefix: "retry_test",
			LogLevel:    "error",
		},
		appLog,
		eventbus.WithRetry(2, 5*time.Millisecond, 10*time.Millisecond),
	)
	require.NoError(t, err)
	defer func() {
		_ = mgr.Close()
	}()

	var attempts atomic.Int32
	recoveredDone := make(chan struct{})

	err = eventbus.Listen(mgr, "flaky_order_handler", func(_ context.Context, ev *orderPaidEvent) error {
		if ev.OrderNo == "POISON" {
			attempts.Add(1)
			return errs.Internal(errors.New("downstream gateway offline")).With("order_no", ev.OrderNo)
		}
		close(recoveredDone)
		return nil
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_ = mgr.Run(ctx)
	}()

	<-mgr.Running()

	// 发送毒丸事件（1 次初始尝试 + 2 次重试 = 共 3 次尝试），耗尽后被 finalErrorBoundary 拦截并终止 GoChannel Nack 重投循环
	require.NoError(t, mgr.Publish(ctx, orderPaidEvent{OrderNo: "POISON"}))

	require.Eventually(t, func() bool {
		return attempts.Load() == 3 &&
			strings.Contains(logBuf.String(), "event handler failed after exhausting retries")
	}, 2*time.Second, 5*time.Millisecond, "毒丸事件应精确执行 1 + 2 = 3 次后由错误边界记录日志")

	// 额外等待一个重试周期，确认 GoChannel 不会因 Nack 发起第 4 次重投
	time.Sleep(25 * time.Millisecond)
	assert.Equal(t, int32(3), attempts.Load(), "错误边界吞掉终态错误后不应再次触发 GoChannel Nack 重投")
	assert.Contains(t, logBuf.String(), `"order_no":"POISON"`)

	// 继续投递正常事件，验证路由器消费正常
	require.NoError(t, mgr.Publish(ctx, orderPaidEvent{OrderNo: "OK-100"}))

	select {
	case <-recoveredDone:
	case <-time.After(3 * time.Second):
		t.Fatal("subsequent healthy event was not processed in time")
	}
}

// TestEventBus_LifecycleAndErrors 验证生命周期边界与错误分支。
func TestEventBus_LifecycleAndErrors(t *testing.T) {
	mgr := eventbus.NewTestManager()

	// 1. 批量/空处理器注册
	require.NoError(t, mgr.RegisterHandler())
	require.NoError(t, mgr.RegisterHandler(nil))
	require.Error(t, eventbus.Listen(nil, "nil_bus", func(context.Context, *orderPaidEvent) error { return nil }))

	// 2. 正常运行与重复运行检测
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		_ = mgr.Run(ctx)
	}()

	<-mgr.Running()

	err := mgr.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already running")

	// 3. 优雅停止与幂等关闭
	require.NoError(t, mgr.Shutdown())
	require.NoError(t, mgr.Close())

	// 关闭后拒绝发布与注册
	require.Error(t, mgr.Publish(context.Background(), orderPaidEvent{OrderNo: "1"}))
	require.Error(t, mgr.RegisterHandler())

	// 4. 未初始化对象调用安全
	var nilMgr *eventbus.Manager
	require.Error(t, nilMgr.Publish(context.Background(), orderPaidEvent{}))
	require.Error(t, nilMgr.RegisterHandler(nil))
	require.NotNil(t, nilMgr.Running())
	<-nilMgr.Running()
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
	err := eventbus.Listen(mgr, "option_test_handler", func(_ context.Context, _ *orderPaidEvent) error {
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

	require.NoError(t, mgr.Publish(ctx, orderPaidEvent{OrderNo: "ORD-1"}))

	select {
	case <-done:
		assert.True(t, middlewareCalled, "自定义中间件应被执行")
	case <-time.After(3 * time.Second):
		t.Fatal("event not handled")
	}
}

// TestLoggerAdapter 验证 Watermill 日志适配器的日志记录与字段下沉能力。
func TestLoggerAdapter(t *testing.T) {
	adapter := eventbus.NewLoggerAdapter(logger.Nop())

	adapter.Info("test info", watermill.LogFields{"key": "val"})
	adapter.Debug("test debug", nil)
	adapter.Trace("test trace", nil)
	adapter.Error("test error", errors.New("sample error"), watermill.LogFields{"err_key": 1})

	withEmpty := adapter.With(nil)
	assert.Equal(t, adapter, withEmpty)

	withFields := adapter.With(watermill.LogFields{"scope": "bus"})
	assert.NotNil(t, withFields)
}
