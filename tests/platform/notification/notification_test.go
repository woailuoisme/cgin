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

	"bgin/internal/config"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/notification"
	"bgin/internal/platform/notification/async"
	"bgin/internal/platform/notification/channel/chat"
	"bgin/internal/platform/notification/channel/mail"
	"bgin/internal/platform/notification/channel/realtime"
	"bgin/internal/platform/notification/channel/sms"
	"bgin/internal/platform/notification/contract"
	"bgin/internal/platform/queue"
	"bgin/internal/shared/notification/database"
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

func (n *mockNotification) Via(notifiable any) []string {
	return []string{"mail", "sms", "chat", "database", "realtime"}
}

func (n *mockNotification) ToMail(notifiable any) any {
	return mail.NewMessage().
		WithSubject("测试邮件").
		WithText(n.content)
}

func (n *mockNotification) ToSMS(notifiable any) any {
	return sms.NewMessage().
		WithPhone("13800138000").
		WithContent(n.content)
}

func (n *mockNotification) ToChat(notifiable any) any {
	return chat.NewMessage().
		WithText(n.content)
}

func (n *mockNotification) ToDatabase(notifiable any) map[string]any {
	return map[string]any{
		"message": n.content,
		"status":  "unread",
	}
}

func (n *mockNotification) ToRealtime(notifiable any) realtime.Message {
	return realtime.Message{
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
	assert.NotNil(t, notifier.SMS())
	assert.NotNil(t, notifier.Chat())

	// Mail、Realtime 与注入型渠道（database）在传入 nil 依赖时不被默认注册
	assert.Nil(t, notifier.Mail())
	assert.Nil(t, notifier.Realtime())
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
	dbCh := database.NewChannel(nil)
	dbPayload, err := dbCh.Format(notif, user)
	require.NoError(t, err)
	dbMsg, ok := dbPayload.(*database.Message)
	require.True(t, ok)
	assert.Equal(t, "系统升级维护提醒", dbMsg.Data["message"])

	// 验证 Paginate 与 PaginateUnread 边界防护 (db=nil 优雅报错)
	_, _, err = dbCh.Paginate(context.Background(), user, 1, 20, false)
	require.Error(t, err)
	_, _, err = dbCh.PaginateUnread(context.Background(), user, 1, 20)
	require.Error(t, err)

	// 2. Mail 渠道格式化
	mailCh := mail.NewChannel(nil)
	mailPayload, err := mailCh.Format(notif, user)
	require.NoError(t, err)
	mailMsg, ok := mailPayload.(*mail.Message)
	require.True(t, ok)
	assert.Equal(t, "测试邮件", mailMsg.Subject)
	assert.Equal(t, "系统升级维护提醒", mailMsg.Text)

	// 3. SMS 渠道格式化
	smsCh := sms.NewChannel("mock")
	smsPayload, err := smsCh.Format(notif, user)
	require.NoError(t, err)
	smsMsg, ok := smsPayload.(*sms.Message)
	require.True(t, ok)
	assert.Equal(t, "系统升级维护提醒", smsMsg.Content)

	// 4. Chat 渠道格式化
	chatCh := chat.NewChannel("telegram")
	chatPayload, err := chatCh.Format(notif, user)
	require.NoError(t, err)
	chatMsg, ok := chatPayload.(*chat.Message)
	require.True(t, ok)
	assert.Equal(t, "系统升级维护提醒", chatMsg.Text)

	// 5. Realtime 渠道格式化
	rtCh := realtime.NewChannel(nil)
	rtPayload, err := rtCh.Format(notif, user)
	require.NoError(t, err)
	rtMsg, ok := rtPayload.(*realtime.Message)
	require.True(t, ok)
	assert.Equal(t, "notification.created", rtMsg.Event)
}

// TestCustomMockChannelSend 验证自定义 Mock 渠道发送与接收人解析链路。
type mockChannel struct {
	sentCount atomic.Int32
}

func (m *mockChannel) Name() string { return "test_mock" }
func (m *mockChannel) Send(ctx context.Context, notifiable any, message any) error {
	m.sentCount.Add(1)
	return nil
}

type singleChannelNotification struct{}

func (s *singleChannelNotification) Via(notifiable any) []string {
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

// TestNotifiableContract 验证 contract.ToNotifiable 智能适配机制。
func TestNotifiableContract(t *testing.T) {
	// 1. 已实现 Notifiable
	u := &mockNotifiable{ID: "custom_id"}
	adapted := contract.ToNotifiable(u)
	require.NotNil(t, adapted)
	assert.Equal(t, "custom_id", adapted.GetNotifiableID())

	// 2. 纯标量 ID（string）自动转为 GenericNotifiable
	strAdapted := contract.ToNotifiable("scalar-user-123")
	require.NotNil(t, strAdapted)
	assert.Equal(t, "scalar-user-123", strAdapted.GetNotifiableID())
	assert.Equal(t, contract.DefaultNotifiableType, strAdapted.GetNotifiableType())

	// 3. nil 实体处理
	nilAdapted := contract.ToNotifiable(nil)
	assert.Nil(t, nilAdapted)
}

// TestAsyncPoolConcurrentSubmitAndClose 验证并发提交与关闭不会 panic，且不再接受新任务。
//
// 关闭在写锁下置位并关闭队列，提交只在读锁下发送，因此不存在
// 「向已关闭通道发送」的窗口。竞态窗口较窄，故多轮重复以提升命中概率。
func TestAsyncPoolConcurrentSubmitAndClose(t *testing.T) {
	ctx := t.Context()

	for range 50 {
		pool := async.New(2, 4)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 100 {
				// 队列满或池已关闭都会返回错误，调用方自行决定降级策略。
				_ = pool.Submit(ctx, func(context.Context) {})
			}
		}()
		go func() {
			defer wg.Done()
			pool.Close()
		}()
		wg.Wait()

		require.ErrorIs(t, pool.Submit(ctx, func(context.Context) {}), async.ErrPoolClosed,
			"关闭后不得再接受新任务")
	}
}

// TestAsyncPoolCloseDrainsBufferedTasks 验证关闭会排空已入队任务而不是将其丢弃。
func TestAsyncPoolCloseDrainsBufferedTasks(t *testing.T) {
	pool := async.New(1, 8)

	// 先占住唯一 worker，迫使后续任务滞留在缓冲区里。
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
	pool := async.New(1, 1)

	blocked := make(chan struct{})
	defer func() {
		close(blocked)
		pool.Close()
	}()

	// 所有任务都阻塞在同一 channel 上，因此吞吐恒为 0：持续提交必然在有限次内撞满队列。
	// 不预设"第几次成功、第几次失败"，避免依赖 worker 抢包的时序。
	var submitErr error
	for range 10 {
		submitErr = pool.Submit(t.Context(), func(context.Context) { <-blocked })
		if submitErr != nil {
			break
		}
	}

	require.ErrorIs(t, submitErr, async.ErrQueueFull,
		"队列满时必须显式回传错误，而不是静默丢弃或另起 goroutine")
}

// TestAsyncPoolPreservesContextMetadata 验证异步任务仍能拿到提交方上下文的元数据，
// 但不会被提交方请求的取消信号中断。
func TestAsyncPoolPreservesContextMetadata(t *testing.T) {
	pool := async.New(1, 8)
	defer pool.Close()

	requestCtx, cancel := context.WithCancel(context.WithValue(t.Context(), asyncTestKey{}, "trace-123"))

	done := make(chan struct{})
	require.NoError(t, pool.Submit(requestCtx, func(asyncCtx context.Context) {
		defer close(done)
		assert.Equal(t, "trace-123", asyncCtx.Value(asyncTestKey{}), "异步任务应保留提交方上下文的元数据")
		assert.NoError(t, asyncCtx.Err(), "异步任务不应继承提交方的取消信号")
	}))

	// 提交后立刻取消请求上下文，任务仍须照常执行完成。
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("异步任务未在超时内执行")
	}
}

// asyncTestKey 是异步池测试专用的上下文键。
type asyncTestKey struct{}

// 声明自定义频控通知
type rateLimitedMockNotif struct {
	key string
}

func (r *rateLimitedMockNotif) Via(target any) []string { return []string{"test_mock"} }
func (r *rateLimitedMockNotif) RateLimitKey(channel string, target any) string {
	return r.key
}

// 声明幂等通知
type idempotentMockNotif struct {
	key string
}

func (d *idempotentMockNotif) Via(target any) []string { return []string{"test_mock"} }
func (d *idempotentMockNotif) IdempotentKey(target any) string {
	return d.key
}

func TestNotificationRateLimitMiddleware(t *testing.T) {
	ctx := t.Context()
	memStore := cache.NewMemoryStore()
	defer func() { _ = memStore.Close() }()

	notifier := notification.NewNotifier(2, 10)
	defer func() { _ = notifier.Close() }()

	mockCh := &mockChannel{}
	notifier.Extend("test_mock", mockCh)

	// 配置频控中间件：最多允许 2 次，窗口 1 分钟
	notifier.Use(notification.RateLimitMiddleware(memStore, 2, time.Minute))

	user := &mockNotifiable{ID: "user_rate_1"}

	// 1. 测试常规通知根据 target ID 自动限流
	notif := &singleChannelNotification{}
	require.NoError(t, notifier.Send(ctx, user, notif))
	require.NoError(t, notifier.Send(ctx, user, notif))

	// 第 3 次应当被限流
	err := notifier.Send(ctx, user, notif)
	require.ErrorIs(t, err, contract.ErrRateLimited)

	// 换一个用户应当不受影响
	userOther := &mockNotifiable{ID: "user_rate_2"}
	require.NoError(t, notifier.Send(ctx, userOther, notif))

	// 2. 测试显式实现 RateLimitedNotification 契约
	customNotif := &rateLimitedMockNotif{key: "custom_vip_sms"}
	require.NoError(t, notifier.Send(ctx, user, customNotif))
	require.NoError(t, notifier.Send(ctx, user, customNotif))

	err = notifier.Send(ctx, user, customNotif)
	require.ErrorIs(t, err, contract.ErrRateLimited)
}

func TestNotificationDeduplicateMiddleware(t *testing.T) {
	ctx := t.Context()
	memStore := cache.NewMemoryStore()
	defer func() { _ = memStore.Close() }()

	notifier := notification.NewNotifier(2, 10)
	defer func() { _ = notifier.Close() }()

	mockCh := &mockChannel{}
	notifier.Extend("test_mock", mockCh)

	// 配置防重幂等中间件
	notifier.Use(notification.DeduplicateMiddleware(memStore, time.Hour))

	user := &mockNotifiable{ID: "user_dedup_1"}

	// 1. 普通通知未声明 IdempotentNotification，多次发送不被拦截
	normalNotif := &singleChannelNotification{}
	require.NoError(t, notifier.Send(ctx, user, normalNotif))
	require.NoError(t, notifier.Send(ctx, user, normalNotif))

	// 2. 幂等通知首次发送成功，重复发送同一 key 被防重拦截
	dedupNotif := &idempotentMockNotif{key: "msg_evt_order_1001"}
	require.NoError(t, notifier.Send(ctx, user, dedupNotif))

	err := notifier.Send(ctx, user, dedupNotif)
	require.ErrorIs(t, err, contract.ErrDeduplicated)

	// 不同的幂等 key 应当成功
	dedupNotif2 := &idempotentMockNotif{key: "msg_evt_order_1002"}
	require.NoError(t, notifier.Send(ctx, user, dedupNotif2))
}

type panicChannel struct{}

func (p *panicChannel) Name() string { return "panic_ch" }
func (p *panicChannel) Send(ctx context.Context, notifiable any, message any) error {
	panic("unexpected internal panic in panicChannel")
}

type panicNotification struct{}

func (p *panicNotification) Via(notifiable any) []string { return []string{"panic_ch"} }

func TestNotificationRecoveryMiddleware(t *testing.T) {
	ctx := t.Context()
	notifier := notification.NewNotifier(2, 10)
	defer func() { _ = notifier.Close() }()

	notifier.Extend("panic_ch", &panicChannel{})

	// 注入 RecoveryMiddleware 捕获异常
	notifier.Use(notification.RecoveryMiddleware())

	user := &mockNotifiable{ID: "user_panic"}
	err := notifier.Send(ctx, user, &panicNotification{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected internal panic")
}

type mockQueueDispatcher struct {
	dispatchedJob  queue.Job
	dispatchedOpts []queue.DispatchOption
}

func (m *mockQueueDispatcher) Dispatch(ctx context.Context, job queue.Job, opts ...queue.DispatchOption) error {
	m.dispatchedJob = job
	m.dispatchedOpts = opts
	return nil
}

type queuedNotification struct {
	taskName string
}

func (q *queuedNotification) Via(target any) []string { return []string{"test_mock"} }
func (q *queuedNotification) ShouldQueue() bool       { return true }
func (q *queuedNotification) QueueName() string       { return "priority_high" }
func (q *queuedNotification) MaxRetries() int         { return 5 }
func (q *queuedNotification) Delay() time.Duration    { return 15 * time.Second }
func (q *queuedNotification) TaskName() string        { return q.taskName }
func (q *queuedNotification) Handle(ctx context.Context) error {
	return nil
}

func TestNotificationWithQueue(t *testing.T) {
	ctx := t.Context()
	notifier := notification.NewNotifier(2, 10)
	defer func() { _ = notifier.Close() }()

	dispatcher := &mockQueueDispatcher{}
	notifier.WithQueue(dispatcher)

	user := &mockNotifiable{ID: "user_queue"}
	jobNotif := &queuedNotification{taskName: "notif:user_welcome"}

	err := notifier.Send(ctx, user, jobNotif)
	require.NoError(t, err)

	require.NotNil(t, dispatcher.dispatchedJob)
	assert.Equal(t, "notif:user_welcome", dispatcher.dispatchedJob.TaskName())
	assert.Len(t, dispatcher.dispatchedOpts, 3)

	var cfg queue.DispatchConfig
	for _, opt := range dispatcher.dispatchedOpts {
		opt(&cfg)
	}
	assert.Equal(t, "priority_high", cfg.QueueName)
	assert.Equal(t, 5, cfg.MaxRetries)
	assert.Equal(t, 15*time.Second, cfg.Delay)
}
