// Package notification 提供统一、模块化、高可扩展的多渠道通知基础设施。
package notification

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"

	"github.com/samber/lo"
	"github.com/samber/oops"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"cgin/internal/platform/errs"
	"cgin/internal/platform/logger"
	"cgin/internal/platform/notification/async"
	"cgin/internal/platform/notification/contract"
	"cgin/internal/platform/queue"
)

// defaultParallelTargets 是并行投递单个通知时的最大并发接收者数。
const defaultParallelTargets = 50

// Notifier 多渠道通知核心引擎。
type Notifier struct {
	mu              sync.RWMutex
	channels        map[string]Channel
	asyncPool       *async.Pool
	queueDispatcher queue.Dispatcher
	middlewares     []Middleware
	parallelTargets int
}

// NewNotifier 创建统一通知引擎实例。
func NewNotifier(workers, queueSize int) *Notifier {
	return &Notifier{
		channels:        make(map[string]Channel),
		asyncPool:       async.New(workers, queueSize),
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

	var dispatchOpts []queue.DispatchOption
	if sqo, ok := notif.(contract.ShouldQueueWithOptions); ok {
		if queueName := sqo.QueueName(); queueName != "" {
			dispatchOpts = append(dispatchOpts, queue.WithQueue(queueName))
		}
		if retries := sqo.MaxRetries(); retries > 0 {
			dispatchOpts = append(dispatchOpts, queue.WithRetries(retries))
		}
		if delay := sqo.Delay(); delay > 0 {
			dispatchOpts = append(dispatchOpts, queue.WithDelay(delay))
		}
	}

	err := q.Dispatch(ctx, job, dispatchOpts...)
	if err == nil {
		return true
	}

	logger.FromContext(ctx).Warn("failed to dispatch notification to queue, falling back to pool",
		zap.Error(err),
		zap.String("notification", fmt.Sprintf("%T", notif)))
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
			logger.FromContext(asyncCtx).Error("failed to deliver async notification",
				zap.Error(err),
				zap.String("notification", fmt.Sprintf("%T", notif)))
		}
	})
	if submitErr == nil {
		return true
	}
	logger.FromContext(ctx).Warn("async notification pool unavailable, falling back to synchronous delivery",
		zap.Error(submitErr),
		zap.Bool("queue_full", errors.Is(submitErr, async.ErrQueueFull)))
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
func formatPayload(ch Channel, notif contract.Notification, target any, chName string) (any, error) {
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
	baseSend := func(innerCtx context.Context, innerTarget any, innerNotif contract.Notification, innerChannel string) error {
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
	collectSend := func(_ context.Context, innerTarget any, innerNotif contract.Notification, innerChannel string) error {
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
