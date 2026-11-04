// Package notification 提供统一、模块化、高可扩展的多渠道通知基础设施。
package notification

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"slices"
	"sync"

	"github.com/hibiken/asynq"
	"github.com/samber/lo"
	"github.com/samber/oops"
	"golang.org/x/sync/errgroup"

	"cgin/internal/config"
	"cgin/pkg/errs"
	"cgin/pkg/logger"
	"cgin/pkg/mailer"
	"cgin/pkg/queue"
	"cgin/pkg/realtime"
)

// 提交失败的原因：由调用方决定是降级同步执行还是丢弃，池自身不替调用方做取舍。
var (
	// ErrPoolClosed 表示任务池已关闭，不再接受新任务。
	ErrPoolClosed = errors.New("async pool is closed")
	// ErrQueueFull 表示缓冲队列已满，调用方应改为同步执行或显式丢弃。
	ErrQueueFull = errors.New("async pool queue is full")
)

// Task 表示在 Worker 池中执行的异步作业。
type Task func(ctx context.Context)

// Pool 管理固定数量的后台 Worker 并发处理任务。
type Pool struct {
	tasks chan Task
	wg    sync.WaitGroup
	once  sync.Once

	// mu 串行化「提交」与「关闭」：发送只在 RLock 下进行，关闭在 Lock 下置位并关闭队列，
	// 因此不存在向已关闭通道发送的窗口；closing 置位后提交一律被拒绝。
	mu      sync.RWMutex
	closing bool
}

// NewPool 创建指定工作协程数和缓冲队列大小的任务池。
func NewPool(workers, queueSize int) *Pool {
	if workers <= 0 {
		workers = 10
	}
	if queueSize <= 0 {
		queueSize = 100
	}

	p := &Pool{tasks: make(chan Task, queueSize)}

	p.wg.Add(workers)
	for range workers {
		go p.worker()
	}

	return p
}

// Submit 提交异步任务。
//
// 传入的 ctx 只用于携带请求级元数据（日志器、追踪链路）：任务执行会被摘除取消信号，
// 因为异步任务的生命周期与触发它的请求无关，请求结束不应中断投递。
func (p *Pool) Submit(ctx context.Context, task Task) error {
	if task == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	baseCtx := context.WithoutCancel(ctx)

	job := func(_ context.Context) {
		defer recoverWithLog(baseCtx)
		task(baseCtx)
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closing {
		return ErrPoolClosed
	}

	select {
	case p.tasks <- job:
		return nil
	default:
		return ErrQueueFull
	}
}

// Close 优雅关闭任务池：先阻断新的提交，再关闭队列让 Worker 把已入队的任务排空后退出。
func (p *Pool) Close() {
	p.once.Do(func() {
		p.mu.Lock()
		p.closing = true
		close(p.tasks)
		p.mu.Unlock()

		p.wg.Wait()
	})
}

func (p *Pool) worker() {
	defer p.wg.Done()
	for task := range p.tasks {
		task(context.Background())
	}
}

func recoverWithLog(ctx context.Context) {
	if r := recover(); r != nil {
		logger.FromContext(ctx).Error().
			Any("panic", r).
			Str("stack", string(debug.Stack())).
			Msg("panic recovered in async notification task execution")
	}
}

// defaultParallelTargets 是并行投递单个通知时的最大并发接收者数。
const defaultParallelTargets = 50

// Notifier 多渠道通知核心引擎。
type Notifier struct {
	mu              sync.RWMutex
	channels        map[string]Channel
	asyncPool       *Pool
	queueDispatcher queue.Dispatcher
	middlewares     []Middleware
	parallelTargets int
}

// New 根据配置与基础依赖构建统一多渠道通知引擎实例。
//
// 需要访问业务表的渠道（如 database）不在此处装配：平台层不得反向依赖
// 业务包，改由 bootstrap 构造后经 extra 参数注入。
func New(cfg config.NotificationConfig, m mailer.Mailer, rt realtime.Publisher, extra ...Channel) (*Notifier, error) {
	poolSize := cmp.Or(cfg.WorkerPool, 10)
	queueSize := cmp.Or(cfg.QueueSize, 100)

	n := NewNotifier(poolSize, queueSize)

	// 1. 邮件渠道 (mail)
	if m != nil {
		n.Extend("mail", NewMailChannel(m))
	}

	// 2. 由调用方注入的渠道（database 等）
	for _, ch := range extra {
		if ch == nil {
			continue
		}
		n.Extend(ch.Name(), ch)
	}

	// 3. 实时广播渠道 (realtime)
	if rt != nil {
		n.Extend("realtime", NewRealtimeChannel(rt))
	}

	// 4. 短信渠道 (sms)
	n.Extend("sms", buildSMSChannel(cfg))

	// 5. 即时通讯渠道 (chat - telegram)
	chatCh := NewChatChannel("telegram")
	if cfg.Telegram.BotToken != "" {
		tgDriver, err := NewTelegramDriver(TelegramConfig{
			BotToken: cfg.Telegram.BotToken,
			ChatID:   cfg.Telegram.ChatID,
			BaseURL:  cfg.Telegram.APIURL,
		})
		if err == nil {
			chatCh.RegisterDriver("telegram", tgDriver)
		}
	}
	n.Extend("chat", chatCh)

	// 6. Gotify 实时推送渠道 (gotify)
	gotifyCh := NewGotifyChannel("http").
		WithDefaults(cfg.Gotify.ServerURL, cfg.Gotify.AppToken, cfg.Gotify.Priority)

	if cfg.Gotify.ServerURL != "" || cfg.Gotify.AppToken != "" {
		httpDriver, err := NewGotifyHTTPDriver(GotifyHTTPConfig{
			ServerURL: cfg.Gotify.ServerURL,
			AppToken:  cfg.Gotify.AppToken,
			Priority:  cfg.Gotify.Priority,
		})
		if err == nil {
			gotifyCh.RegisterDriver("http", httpDriver)
		}
	}
	n.Extend("gotify", gotifyCh)

	return n, nil
}

// buildSMSChannel 按配置装配短信渠道并注册全部可用驱动。
// 驱动构造失败（凭据不完整）时静默跳过：配置校验已在启动期拦下必填项缺失。
func buildSMSChannel(cfg config.NotificationConfig) *SMSChannel {
	smsCh := NewSMSChannel(cmp.Or(cfg.SMS.NormalizedDriver(), "log"))
	smsCh.RegisterDriver("log", NewLogSMSDriver())

	if cfg.SMS.Aliyun.AccessKeyID != "" && cfg.SMS.Aliyun.AccessKeySecret != "" {
		driver, err := NewAliyunSMSDriver(AliyunSMSConfig{
			AccessKeyID:     cfg.SMS.Aliyun.AccessKeyID,
			AccessKeySecret: cfg.SMS.Aliyun.AccessKeySecret,
			SignName:        cmp.Or(cfg.SMS.Aliyun.SignName, cfg.SMS.DefaultSignName),
			TemplateCode:    cfg.SMS.Aliyun.TemplateCode,
		})
		if err == nil {
			smsCh.RegisterDriver("aliyun", driver)
		}
	}

	if cfg.SMS.Tencent.SecretID != "" && cfg.SMS.Tencent.SecretKey != "" {
		driver, err := NewTencentSMSDriver(TencentSMSConfig{
			SecretID:   cfg.SMS.Tencent.SecretID,
			SecretKey:  cfg.SMS.Tencent.SecretKey,
			AppID:      cfg.SMS.Tencent.AppID,
			SignName:   cmp.Or(cfg.SMS.Tencent.SignName, cfg.SMS.DefaultSignName),
			TemplateID: cfg.SMS.Tencent.TemplateID,
			Region:     cfg.SMS.Tencent.Region,
		})
		if err == nil {
			smsCh.RegisterDriver("tencent", driver)
		}
	}

	if cfg.SMS.SMSBao.Username != "" && cfg.SMS.SMSBao.APIKey != "" {
		driver, err := NewSMSBaoDriver(SMSBaoConfig{
			Username: cfg.SMS.SMSBao.Username,
			APIKey:   cfg.SMS.SMSBao.APIKey,
			SignName: cmp.Or(cfg.SMS.SMSBao.SignName, cfg.SMS.DefaultSignName),
			Template: cfg.SMS.SMSBao.Template,
		})
		if err == nil {
			smsCh.RegisterDriver("smsbao", driver)
		}
	}

	return smsCh
}

// NewNotifier 创建统一通知引擎实例。
func NewNotifier(workers, queueSize int) *Notifier {
	return &Notifier{
		channels:        make(map[string]Channel),
		asyncPool:       NewPool(workers, queueSize),
		parallelTargets: defaultParallelTargets,
	}
}

// WithQueue 注入持久化队列分发器（符合 DIP 原则）。
func (n *Notifier) WithQueue(dispatcher queue.Dispatcher) *Notifier {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.queueDispatcher = dispatcher
	return n
}

// Use 注册通知发送管道中间件（支持洋葱圈模式）。
func (n *Notifier) Use(mw ...Middleware) *Notifier {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.middlewares = append(n.middlewares, mw...)
	return n
}

// Extend 注册自定义渠道。
func (n *Notifier) Extend(name string, ch Channel) *Notifier {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.channels[name] = ch
	return n
}

// WithParallelTargets 设置批量通知时并行投递的最大并发接收者数，非正值将被忽略。
func (n *Notifier) WithParallelTargets(limit int) *Notifier {
	if limit > 0 {
		n.mu.Lock()
		defer n.mu.Unlock()
		n.parallelTargets = limit
	}
	return n
}

// parallelLimit 返回当前并行上限，未配置时回退默认值。
func (n *Notifier) parallelLimit() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return lo.Ternary(n.parallelTargets > 0, n.parallelTargets, defaultParallelTargets)
}

// Channel 按名称查找已注册的渠道。
func (n *Notifier) Channel(name string) Channel {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.channels[name]
}

// Close 优雅停止通知引擎及其异步 Worker 池。
func (n *Notifier) Close() error {
	if n.asyncPool != nil {
		n.asyncPool.Close()
	}
	return nil
}

// Shutdown 释放工作协程池，满足 do.ShutdownerWithError 契约。
func (n *Notifier) Shutdown() error {
	return n.Close()
}

// toSlice 将单个实体或切片归一化为 []any。
func toSlice(target any) []any {
	if target == nil {
		return nil
	}
	if s, ok := target.([]any); ok {
		return s
	}
	val := reflect.ValueOf(target)
	if val.Kind() == reflect.Slice || val.Kind() == reflect.Array {
		result := make([]any, val.Len())
		for i := range val.Len() {
			result[i] = val.Index(i).Interface()
		}
		return result
	}
	return []any{target}
}

// parseSendOptions 解析单次发送参数。
func parseSendOptions(opts []SendOption) SendOptions {
	var so SendOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&so)
		}
	}
	return so
}

// shouldRunAsync 判断当前通知是否应当异步入队。
func shouldRunAsync(notif Notification, so *SendOptions) bool {
	if so.SyncSet {
		return !so.Sync
	}
	if sq, ok := notif.(ShouldQueue); ok {
		return sq.ShouldQueue()
	}
	return false
}

// Send 按顺序向指定接收者列表同步或异步投递通知。
func (n *Notifier) Send(ctx context.Context, notifiables any, notif Notification, opts ...SendOption) error {
	if notif == nil {
		return nil
	}

	targets := toSlice(notifiables)
	if len(targets) == 0 {
		return nil
	}

	so := parseSendOptions(opts)

	if shouldRunAsync(notif, &so) && n.tryDispatchAsync(ctx, notif, func(asyncCtx context.Context) error {
		return n.sendSync(asyncCtx, targets, notif, &so)
	}) {
		return nil
	}

	return n.sendSync(ctx, targets, notif, &so)
}

// dispatchQueueJob 尝试将实现了 queue.Job 的通知直接派发至持久化队列。
func dispatchQueueJob(ctx context.Context, q queue.Dispatcher, notif Notification) bool {
	job, ok := notif.(queue.Job)
	if !ok {
		return false
	}

	var dispatchOpts []asynq.Option
	if sqo, ok := notif.(ShouldQueueWithOptions); ok {
		if queueName := sqo.QueueName(); queueName != "" {
			dispatchOpts = append(dispatchOpts, asynq.Queue(queueName))
		}
		if retries := sqo.MaxRetries(); retries > 0 {
			dispatchOpts = append(dispatchOpts, asynq.MaxRetry(retries))
		}
		if delay := sqo.Delay(); delay > 0 {
			dispatchOpts = append(dispatchOpts, asynq.ProcessIn(delay))
		}
	}

	err := q.Dispatch(ctx, job, dispatchOpts...)
	if err == nil {
		return true
	}

	logger.FromContext(ctx).Warn().
		Err(err).
		Str("notification", fmt.Sprintf("%T", notif)).
		Msg("failed to dispatch notification to queue, falling back to pool")
	return false
}

// tryDispatchAsync 尝试优先提交给持久化队列处理；若未配置或失败则降级提交给协程池；若均不可用则返回 false 回退为同步处理。
func (n *Notifier) tryDispatchAsync(ctx context.Context, notif Notification, task func(asyncCtx context.Context) error) bool {
	n.mu.RLock()
	q := n.queueDispatcher
	n.mu.RUnlock()

	if q != nil && dispatchQueueJob(ctx, q, notif) {
		return true
	}

	if n.asyncPool == nil {
		return false
	}
	submitErr := n.asyncPool.Submit(ctx, func(asyncCtx context.Context) {
		if err := task(asyncCtx); err != nil {
			logger.FromContext(asyncCtx).Error().
				Err(err).
				Str("notification", fmt.Sprintf("%T", notif)).
				Msg("failed to deliver async notification")
		}
	})
	if submitErr == nil {
		return true
	}
	logger.FromContext(ctx).Warn().
		Err(submitErr).
		Bool("queue_full", errors.Is(submitErr, ErrQueueFull)).
		Msg("async notification pool unavailable, falling back to synchronous delivery")
	return false
}

// resolveChannels 解析当前接收者需要投递的目标渠道列表。
func resolveChannels(notif Notification, target any, so *SendOptions) []string {
	if len(so.Channels) > 0 {
		return so.Channels
	}
	return notif.Via(target)
}

// batchCollector 聚合支持 BatchChannel 的同渠道批量接收者与格式化载荷。
type batchCollector struct {
	mu       sync.Mutex
	ch       BatchChannel
	targets  []any
	messages []any
}

// batchRegistry 线程安全地按渠道名称管理批量收集器。
type batchRegistry struct {
	mu       sync.Mutex
	capacity int
	batches  map[string]*batchCollector
	order    []string
}

func (r *batchRegistry) resolve(n *Notifier, chName string) (*batchCollector, bool) {
	if r.capacity <= 1 {
		return nil, false
	}
	batchCh, ok := n.Channel(chName).(BatchChannel)
	if !ok {
		return nil, false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.batches == nil {
		r.batches = make(map[string]*batchCollector, 2)
	}
	collector, exists := r.batches[chName]
	if !exists {
		collector = &batchCollector{
			ch:       batchCh,
			targets:  make([]any, 0, r.capacity),
			messages: make([]any, 0, r.capacity),
		}
		r.batches[chName] = collector
		r.order = append(r.order, chName)
	}
	return collector, true
}

func (r *batchRegistry) flush(ctx context.Context) error {
	for _, chName := range r.order {
		collector := r.batches[chName]
		if len(collector.targets) == 0 {
			continue
		}
		if err := collector.ch.SendBatch(ctx, collector.targets, collector.messages); err != nil {
			return oops.Code(errs.CodeNotificationSendFailed).
				With("channel", chName).
				Wrap(err)
		}
	}
	return nil
}

// formatPayload 将抽象通知转换为渠道专属载荷。
func formatPayload(ch Channel, notif Notification, target any, chName string) (any, error) {
	formatter, ok := ch.(Formatter)
	if !ok {
		return notif, nil
	}
	message, err := formatter.Format(notif, target)
	if err != nil {
		return nil, oops.Code(errs.CodeNotificationFormatFailed).
			With("channel", chName).
			Wrap(err)
	}
	return message, nil
}

// dispatchToChannel 针对单一渠道格式化并投递通知载荷，经由注册的中间件管道处理。
func (n *Notifier) dispatchToChannel(ctx context.Context, target any, notif Notification, chName string) error {
	baseSend := func(innerCtx context.Context, innerTarget any, innerNotif Notification, innerChannel string) error {
		ch := n.Channel(innerChannel)
		if ch == nil {
			return oops.Code(errs.CodeNotificationChannelNotFound).
				With("channel", innerChannel).
				Errorf("unsupported notification channel: %s", innerChannel)
		}

		message, err := formatPayload(ch, innerNotif, innerTarget, innerChannel)
		if err != nil {
			return err
		}

		if err := ch.Send(innerCtx, innerTarget, message); err != nil {
			return oops.Code(errs.CodeNotificationSendFailed).
				With("channel", innerChannel).
				Wrap(err)
		}
		return nil
	}

	n.mu.RLock()
	mws := slices.Clone(n.middlewares)
	n.mu.RUnlock()

	pipeline := WrapMiddleware(baseSend, mws...)
	return pipeline(ctx, target, notif, chName)
}

// collectForBatch 经由中间件管道校验与格式化后，将目标与载荷收集至批量缓冲器。
func (n *Notifier) collectForBatch(ctx context.Context, target any, notif Notification, chName string, collector *batchCollector) error {
	collectSend := func(_ context.Context, innerTarget any, innerNotif Notification, innerChannel string) error {
		message, err := formatPayload(collector.ch, innerNotif, innerTarget, innerChannel)
		if err != nil {
			return err
		}
		collector.mu.Lock()
		collector.targets = append(collector.targets, innerTarget)
		collector.messages = append(collector.messages, message)
		collector.mu.Unlock()
		return nil
	}

	n.mu.RLock()
	mws := slices.Clone(n.middlewares)
	n.mu.RUnlock()

	pipeline := WrapMiddleware(collectSend, mws...)
	return pipeline(ctx, target, notif, chName)
}

// sendSync 内部同步执行投递循环；对实现 BatchChannel 的渠道自动合并为单次批量往返。
func (n *Notifier) sendSync(ctx context.Context, targets []any, notif Notification, so *SendOptions) error {
	reg := batchRegistry{capacity: len(targets)}

	for _, target := range targets {
		for _, chName := range resolveChannels(notif, target, so) {
			if collector, ok := reg.resolve(n, chName); ok {
				if err := n.collectForBatch(ctx, target, notif, chName, collector); err != nil {
					return err
				}
				continue
			}
			if err := n.dispatchToChannel(ctx, target, notif, chName); err != nil {
				return err
			}
		}
	}

	return reg.flush(ctx)
}

// SendParallel 在接收者与渠道维度并发投递通知，并限制最大并发数。
func (n *Notifier) SendParallel(ctx context.Context, notifiables any, notif Notification, opts ...SendOption) error {
	if notif == nil {
		return nil
	}

	targets := toSlice(notifiables)
	if len(targets) == 0 {
		return nil
	}

	so := parseSendOptions(opts)

	if shouldRunAsync(notif, &so) && n.tryDispatchAsync(ctx, notif, func(asyncCtx context.Context) error {
		return n.sendParallelSync(asyncCtx, targets, notif, &so)
	}) {
		return nil
	}

	return n.sendParallelSync(ctx, targets, notif, &so)
}

func (n *Notifier) sendParallelSync(ctx context.Context, targets []any, notif Notification, so *SendOptions) error {
	reg := batchRegistry{capacity: len(targets)}

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(n.parallelLimit())

	for _, target := range targets {
		for _, chName := range resolveChannels(notif, target, so) {
			if collector, ok := reg.resolve(n, chName); ok {
				g.Go(func() error {
					return n.collectForBatch(gCtx, target, notif, chName, collector)
				})
				continue
			}
			g.Go(func() error {
				return n.dispatchToChannel(gCtx, target, notif, chName)
			})
		}
	}

	if err := g.Wait(); err != nil {
		return err
	}

	return reg.flush(ctx)
}
