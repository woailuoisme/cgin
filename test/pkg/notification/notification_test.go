// Package notification_test 针对通知系统基础设施进行全方位单元契约测试。
package notification_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/internal/feature/client/auth/store"
	"cgin/pkg/cache"
	"cgin/pkg/notification"
	"cgin/pkg/queue"
)

// mockNotifiable 模拟实现 Notifiable 契约的测试用户实体。
type mockNotifiable struct {
	ID    string
	Email string
	Phone string
	TGID  string
}

func (m *mockNotifiable) GetNotifiableID() string {
	return m.ID
}

func (m *mockNotifiable) GetNotifiableType() string {
	return "user"
}

func (m *mockNotifiable) RouteNotificationFor(channel string) string {
	switch channel {
	case "mail":
		return m.Email
	case "sms":
		return m.Phone
	case "chat", "telegram":
		return m.TGID
	default:
		return ""
	}
}

// mockNotification 模拟一个全渠道领域通知。
type mockNotification struct {
	content string
}

func (n *mockNotification) Via(_ any) []string {
	return []string{"mail", "sms", "chat", "database", "realtime"}
}

func (n *mockNotification) ToMail(_ any) any {
	return notification.NewMailMessage().
		WithSubject("测试邮件").
		WithText(n.content)
}

func (n *mockNotification) ToSMS(_ any) any {
	return notification.NewSMSMessage().
		WithPhone("13800138000").
		WithContent(n.content)
}

func (n *mockNotification) ToChat(_ any) any {
	return notification.NewChatMessage().
		WithText(n.content)
}

func (n *mockNotification) ToDatabase(_ any) map[string]any {
	return map[string]any{
		"message": n.content,
		"status":  "unread",
	}
}

func (n *mockNotification) ToRealtime(_ any) notification.RealtimeMessage {
	return notification.RealtimeMessage{
		Event: "notification.created",
		Data:  map[string]any{"content": n.content},
	}
}

// TestNotificationAssembly 验证通过 config.NotificationConfig 装配统一通知管理器。
func TestNotificationAssembly(t *testing.T) {
	cfg := config.NotificationConfig{
		WorkerPool: 5,
		QueueSize:  50,
		SMS: config.NotificationSMSConfig{
			Driver: "mock",
		},
		Telegram: config.NotificationTelegramConfig{
			BotToken: "mock-token",
			ChatID:   "12345678",
		},
	}

	notifier, err := notification.New(cfg, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, notifier)
	defer func() {
		_ = notifier.Close()
	}()

	// 验证各渠道实例可正常获取
	assert.NotNil(t, notifier.Channel("sms"))
	assert.NotNil(t, notifier.Channel("chat"))

	// Mail、Realtime 与注入型渠道（database）在传入 nil 依赖时不被默认注册
	assert.Nil(t, notifier.Channel("mail"))
	assert.Nil(t, notifier.Channel("realtime"))
	assert.Nil(t, notifier.Channel("database"))
}

// TestChannelFormatting 验证各渠道针对强类型通知的载荷格式化。
func TestChannelFormatting(t *testing.T) {
	user := &mockNotifiable{
		ID:    "user_100",
		Email: "test@example.com",
		Phone: "+8613800138000",
		TGID:  "tg_8888",
	}
	notif := &mockNotification{content: "系统升级维护提醒"}

	// 1. Database 渠道格式化
	dbCh := store.NewChannel(nil)
	dbPayload, err := dbCh.Format(notif, user)
	require.NoError(t, err)
	dbMsg, ok := dbPayload.(*store.Message)
	require.True(t, ok)
	assert.Equal(t, "系统升级维护提醒", dbMsg.Data["message"])

	// 验证 Paginate 与 PaginateUnread 边界防护 (db=nil 优雅报错)
	_, _, err = dbCh.Paginate(context.Background(), user, 1, 20, false)
	require.Error(t, err)
	_, _, err = dbCh.PaginateUnread(context.Background(), user, 1, 20)
	require.Error(t, err)

	// 2. Mail 渠道格式化
	mailCh := notification.NewMailChannel(nil)
	mailPayload, err := mailCh.Format(notif, user)
	require.NoError(t, err)
	mailMsg, ok := mailPayload.(*notification.MailMessage)
	require.True(t, ok)
	assert.Equal(t, "测试邮件", mailMsg.Subject)
	assert.Equal(t, "系统升级维护提醒", mailMsg.Text)

	// 3. SMS 渠道格式化
	smsCh := notification.NewSMSChannel("mock")
	smsPayload, err := smsCh.Format(notif, user)
	require.NoError(t, err)
	smsMsg, ok := smsPayload.(*notification.SMSMessage)
	require.True(t, ok)
	assert.Equal(t, "系统升级维护提醒", smsMsg.Content)

	// 4. Chat 渠道格式化
	chatCh := notification.NewChatChannel("telegram")
	chatPayload, err := chatCh.Format(notif, user)
	require.NoError(t, err)
	chatMsg, ok := chatPayload.(*notification.ChatMessage)
	require.True(t, ok)
	assert.Equal(t, "系统升级维护提醒", chatMsg.Text)

	// 5. Realtime 渠道格式化
	rtCh := notification.NewRealtimeChannel(nil)
	rtPayload, err := rtCh.Format(notif, user)
	require.NoError(t, err)
	rtMsg, ok := rtPayload.(*notification.RealtimeMessage)
	require.True(t, ok)
	assert.Equal(t, "notification.created", rtMsg.Event)
}

// TestCustomMockChannelSend 验证自定义 Mock 渠道发送与接收人解析链路。
type mockChannel struct {
	sentCount atomic.Int32
}

func (m *mockChannel) Name() string { return "test_mock" }
func (m *mockChannel) Send(_ context.Context, _ any, _ any) error {
	m.sentCount.Add(1)
	return nil
}

type singleChannelNotification struct{}

func (s *singleChannelNotification) Via(_ any) []string {
	return []string{"test_mock"}
}

func TestNotifierSendAndParallel(t *testing.T) {
	ctx := t.Context()
	n := notification.NewNotifier(2, 10)
	defer func() { _ = n.Close() }()

	mockCh := &mockChannel{}
	n.Extend("test_mock", mockCh)

	user1 := &mockNotifiable{ID: "u1"}
	user2 := &mockNotifiable{ID: "u2"}
	notif := &singleChannelNotification{}

	// 同步发送
	err := n.Send(ctx, []any{user1, user2}, notif)
	require.NoError(t, err)
	assert.Equal(t, int32(2), mockCh.sentCount.Load())

	// 并发发送
	err = n.SendParallel(ctx, []any{user1, user2}, notif)
	require.NoError(t, err)
	assert.Equal(t, int32(4), mockCh.sentCount.Load())
}

// TestAsyncNotificationQueue 验证异步队列在有界并发池中的执行与安全关闭。
func TestAsyncNotificationQueue(t *testing.T) {
	ctx := t.Context()
	n := notification.NewNotifier(4, 20)

	mockCh := &mockChannel{}
	n.Extend("test_mock", mockCh)

	user := &mockNotifiable{ID: "u_async"}
	notif := &singleChannelNotification{}

	for range 10 {
		err := n.Send(ctx, user, notif, notification.Async())
		require.NoError(t, err)
	}

	// 稍候等待 worker 池消费完成
	assert.Eventually(t, func() bool {
		return mockCh.sentCount.Load() == 10
	}, 2*time.Second, 20*time.Millisecond)

	// 优雅关闭
	err := n.Close()
	require.NoError(t, err)
}

// TestNotifiableContract 验证 notification.ToNotifiable 智能适配机制。
func TestNotifiableContract(t *testing.T) {
	// 1. 已实现 Notifiable
	u := &mockNotifiable{ID: "custom_id"}
	adapted := notification.ToNotifiable(u)
	require.NotNil(t, adapted)
	assert.Equal(t, "custom_id", adapted.GetNotifiableID())

	// 2. 纯标量 ID（string）自动转为 GenericNotifiable
	strAdapted := notification.ToNotifiable("scalar-user-123")
	require.NotNil(t, strAdapted)
	assert.Equal(t, "scalar-user-123", strAdapted.GetNotifiableID())
	assert.Equal(t, notification.DefaultNotifiableType, strAdapted.GetNotifiableType())

	// 3. nil 实体处理
	nilAdapted := notification.ToNotifiable(nil)
	assert.Nil(t, nilAdapted)
}

// TestAsyncPoolConcurrentSubmitAndClose 验证并发提交与关闭不会 panic，且不再接受新任务。
func TestAsyncPoolConcurrentSubmitAndClose(t *testing.T) {
	ctx := t.Context()

	for range 50 {
		pool := notification.NewPool(2, 4)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = pool.Submit(ctx, func(context.Context) {})
			}
		}()
		go func() {
			defer wg.Done()
			pool.Close()
		}()
		wg.Wait()

		require.ErrorIs(t, pool.Submit(ctx, func(context.Context) {}), notification.ErrPoolClosed,
			"关闭后不得再接受新任务")
	}
}

// TestAsyncPoolCloseDrainsBufferedTasks 验证关闭会排空已入队任务而不是将其丢弃。
func TestAsyncPoolCloseDrainsBufferedTasks(t *testing.T) {
	pool := notification.NewPool(1, 8)

	blocked := make(chan struct{})
	require.NoError(t, pool.Submit(t.Context(), func(context.Context) { <-blocked }))

	var executed atomic.Int64
	for range 4 {
		require.NoError(t, pool.Submit(t.Context(), func(context.Context) { executed.Add(1) }))
	}

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		pool.Close()
	}()

	close(blocked)

	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("关闭超时，缓冲任务可能被丢弃或 worker 未退出")
	}

	assert.Equal(t, int64(4), executed.Load(), "关闭前已入队的任务必须全部执行完毕")
}

// TestAsyncPoolReportsFullQueue 验证队列满时以错误回传，交由调用方降级。
func TestAsyncPoolReportsFullQueue(t *testing.T) {
	pool := notification.NewPool(1, 1)

	blocked := make(chan struct{})
	defer func() {
		close(blocked)
		pool.Close()
	}()

	var submitErr error
	for range 10 {
		submitErr = pool.Submit(t.Context(), func(context.Context) { <-blocked })
		if submitErr != nil {
			break
		}
	}

	require.ErrorIs(t, submitErr, notification.ErrQueueFull,
		"队列满时必须显式回传错误，而不是静默丢弃或另起 goroutine")
}

// TestAsyncPoolPreservesContextMetadata 验证异步任务仍能拿到提交方上下文的元数据，
// 但不会被提交方请求的取消信号中断。
func TestAsyncPoolPreservesContextMetadata(t *testing.T) {
	pool := notification.NewPool(1, 8)
	defer pool.Close()

	requestCtx, cancel := context.WithCancel(context.WithValue(t.Context(), asyncTestKey{}, "trace-123"))

	done := make(chan struct{})
	require.NoError(t, pool.Submit(requestCtx, func(asyncCtx context.Context) {
		defer close(done)
		assert.Equal(t, "trace-123", asyncCtx.Value(asyncTestKey{}), "异步任务应保留提交方上下文的元数据")
		assert.NoError(t, asyncCtx.Err(), "异步任务不应继承提交方的取消信号")
	}))

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("异步任务未在超时内执行")
	}
}

type asyncTestKey struct{}

type rateLimitedMockNotif struct {
	key string
}

func (r *rateLimitedMockNotif) Via(_ any) []string { return []string{"test_mock"} }
func (r *rateLimitedMockNotif) RateLimitKey(_ string, _ any) string {
	return r.key
}

type idempotentMockNotif struct {
	key string
}

func (d *idempotentMockNotif) Via(_ any) []string         { return []string{"test_mock"} }
func (d *idempotentMockNotif) IdempotentKey(_ any) string { return d.key }

func TestNotificationRateLimitMiddleware(t *testing.T) {
	ctx := t.Context()
	memStore := cache.NewMemoryStore()
	defer func() { _ = memStore.Close() }()

	notifier := notification.NewNotifier(2, 10)
	defer func() { _ = notifier.Close() }()

	mockCh := &mockChannel{}
	notifier.Extend("test_mock", mockCh)
	notifier.Use(notification.RateLimitMiddleware(memStore, 2, time.Minute))

	user := &mockNotifiable{ID: "user_rate_1"}

	notif := &singleChannelNotification{}
	require.NoError(t, notifier.Send(ctx, user, notif))
	require.NoError(t, notifier.Send(ctx, user, notif))

	err := notifier.Send(ctx, user, notif)
	require.ErrorIs(t, err, notification.ErrRateLimited)

	userOther := &mockNotifiable{ID: "user_rate_2"}
	require.NoError(t, notifier.Send(ctx, userOther, notif))

	customNotif := &rateLimitedMockNotif{key: "custom_vip_sms"}
	require.NoError(t, notifier.Send(ctx, user, customNotif))
	require.NoError(t, notifier.Send(ctx, user, customNotif))

	err = notifier.Send(ctx, user, customNotif)
	require.ErrorIs(t, err, notification.ErrRateLimited)
}

func TestNotificationDeduplicateMiddleware(t *testing.T) {
	ctx := t.Context()
	memStore := cache.NewMemoryStore()
	defer func() { _ = memStore.Close() }()

	notifier := notification.NewNotifier(2, 10)
	defer func() { _ = notifier.Close() }()

	mockCh := &mockChannel{}
	notifier.Extend("test_mock", mockCh)
	notifier.Use(notification.DeduplicateMiddleware(memStore, time.Hour))

	user := &mockNotifiable{ID: "user_dedup_1"}

	normalNotif := &singleChannelNotification{}
	require.NoError(t, notifier.Send(ctx, user, normalNotif))
	require.NoError(t, notifier.Send(ctx, user, normalNotif))

	dedupNotif := &idempotentMockNotif{key: "msg_evt_order_1001"}
	require.NoError(t, notifier.Send(ctx, user, dedupNotif))

	err := notifier.Send(ctx, user, dedupNotif)
	require.ErrorIs(t, err, notification.ErrDeduplicated)

	dedupNotif2 := &idempotentMockNotif{key: "msg_evt_order_1002"}
	require.NoError(t, notifier.Send(ctx, user, dedupNotif2))
}

type panicChannel struct{}

func (p *panicChannel) Name() string { return "panic_ch" }
func (p *panicChannel) Send(_ context.Context, _ any, _ any) error {
	panic("unexpected internal panic in panicChannel")
}

type panicNotification struct{}

func (p *panicNotification) Via(_ any) []string { return []string{"panic_ch"} }

func TestNotificationRecoveryMiddleware(t *testing.T) {
	ctx := t.Context()
	notifier := notification.NewNotifier(2, 10)
	defer func() { _ = notifier.Close() }()

	notifier.Extend("panic_ch", &panicChannel{})
	notifier.Use(notification.RecoveryMiddleware())

	user := &mockNotifiable{ID: "user_panic"}
	err := notifier.Send(ctx, user, &panicNotification{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected internal panic")
}

type queuedNotification struct {
	taskName string
}

func (q *queuedNotification) Via(_ any) []string   { return []string{"test_mock"} }
func (q *queuedNotification) ShouldQueue() bool    { return true }
func (q *queuedNotification) QueueName() string    { return "priority_high" }
func (q *queuedNotification) MaxRetries() int      { return 5 }
func (q *queuedNotification) Delay() time.Duration { return 15 * time.Second }
func (q *queuedNotification) TaskName() string     { return q.taskName }

func TestNotificationWithQueue(t *testing.T) {
	ctx := t.Context()
	notifier := notification.NewNotifier(2, 10)
	defer func() { _ = notifier.Close() }()

	producer := queue.NewMemoryProducer()
	notifier.WithQueue(producer)

	user := &mockNotifiable{ID: "user_queue"}
	jobNotif := &queuedNotification{taskName: "notif:user_welcome"}

	err := notifier.Send(ctx, user, jobNotif)
	require.NoError(t, err)

	require.Equal(t, 1, producer.Count())
	tasks := producer.Tasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "notif:user_welcome", tasks[0].TaskName)
	assert.Equal(t, "priority_high", tasks[0].Settings.Queue)
	assert.Equal(t, 5, tasks[0].Settings.MaxRetry)
	assert.Equal(t, 15*time.Second, tasks[0].Settings.Delay)
}

type mockBatchChannel struct {
	singleCalls atomic.Int32
	batchCalls  atomic.Int32
	batchSizes  []int
}

func (m *mockBatchChannel) Name() string { return "batch_mock" }

func (m *mockBatchChannel) Send(_ context.Context, _ any, _ any) error {
	m.singleCalls.Add(1)
	return nil
}

func (m *mockBatchChannel) SendBatch(_ context.Context, notifiables []any, _ []any) error {
	m.batchCalls.Add(1)
	m.batchSizes = append(m.batchSizes, len(notifiables))
	return nil
}

type batchTestNotification struct{}

func (b *batchTestNotification) Via(_ any) []string { return []string{"batch_mock"} }

func TestNotifierBatchChannel(t *testing.T) {
	ctx := t.Context()
	n := notification.NewNotifier(2, 10)
	defer func() { _ = n.Close() }()

	batchCh := &mockBatchChannel{}
	n.Extend("batch_mock", batchCh)

	targets := []any{
		&mockNotifiable{ID: "u1"},
		&mockNotifiable{ID: "u2"},
		&mockNotifiable{ID: "u3"},
	}
	notif := &batchTestNotification{}

	require.NoError(t, n.Send(ctx, targets, notif))
	assert.Equal(t, int32(0), batchCh.singleCalls.Load())
	assert.Equal(t, int32(1), batchCh.batchCalls.Load())
	assert.Equal(t, []int{3}, batchCh.batchSizes)

	require.NoError(t, n.SendParallel(ctx, targets, notif))
	assert.Equal(t, int32(0), batchCh.singleCalls.Load())
	assert.Equal(t, int32(2), batchCh.batchCalls.Load())
	assert.Equal(t, []int{3, 3}, batchCh.batchSizes)
}
