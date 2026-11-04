// Package eventbus 提供基于 Watermill CQRS 的强类型领域事件总线、路由管道与中间件生命周期管理。
package eventbus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"

	"cgin/internal/config"
	"cgin/pkg/logger"
)

// DriverGoChannel 代表基于进程内内存通道的默认事件传输驱动。
const DriverGoChannel = "go_channel"

// Event 是领域事件的统一强类型契约接口（值接收者实现，使 T 与 *T 均满足契约）。
type Event interface {
	EventName() string
}

// Publisher 是业务层发布领域事件的统一契约接口。
type Publisher interface {
	Publish(ctx context.Context, event Event) error
}

// EventHandler 定义领域事件处理器的统一契约。
type EventHandler = cqrs.EventHandler

// Subscriber 是事件监听器注册与声明的契约接口。
type Subscriber interface {
	RegisterHandler(handlers ...EventHandler) error
}

type nopPublisher struct{}

var defaultNopPublisher Publisher = nopPublisher{}

func (nopPublisher) Publish(context.Context, Event) error { return nil }

// NopPublisher 返回静默丢弃所有事件的空发布器，作为可选 Publisher 依赖的默认兜底。
func NopPublisher() Publisher {
	return defaultNopPublisher
}

// Listen 使用 Go 泛型向事件总线声明式注册强类型领域事件监听器。
func Listen[T Event](bus Subscriber, name string, handler func(ctx context.Context, event *T) error) error {
	if bus == nil {
		return fmt.Errorf("eventbus: cannot register handler %q on nil subscriber", name)
	}
	return bus.RegisterHandler(cqrs.NewEventHandler(name, handler))
}

var (
	_ Publisher  = (*Manager)(nil)
	_ Subscriber = (*Manager)(nil)
)

// Option 为 Manager 构造提供函数式配置选项。
type Option func(*Manager)

// Manager 封装基于 Watermill CQRS 的事件总线、路由器与事件处理器生命周期。
type Manager struct {
	cfg            config.EventBusConfig
	baseLogger     *slog.Logger
	pubSub         *gochannel.GoChannel
	router         *message.Router
	eventBus       *cqrs.EventBus
	eventProcessor *cqrs.EventProcessor

	closeTimeout         time.Duration
	retryMax             int
	retryInitialInterval time.Duration
	retryMaxInterval     time.Duration
	handlerTimeout       time.Duration
	customMiddlewares    []message.HandlerMiddleware

	mu      sync.Mutex
	running bool
	closed  atomic.Bool
}

// WithCloseTimeout 设置事件路由器优雅退出的排空超时时间。
func WithCloseTimeout(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.closeTimeout = d
		}
	}
}

// WithRetry 配置事件处理器的失败重试策略。
func WithRetry(maxRetries int, initialInterval, maxInterval time.Duration) Option {
	return func(m *Manager) {
		if maxRetries >= 0 {
			m.retryMax = maxRetries
		}
		if initialInterval > 0 {
			m.retryInitialInterval = initialInterval
		}
		if maxInterval > 0 {
			m.retryMaxInterval = maxInterval
		}
	}
}

// WithHandlerTimeout 设置单个事件处理器的执行超时限制。
func WithHandlerTimeout(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.handlerTimeout = d
		}
	}
}

// WithMiddleware 向路由器管道追加自定义中间件。
func WithMiddleware(middlewares ...message.HandlerMiddleware) Option {
	return func(m *Manager) {
		m.customMiddlewares = append(m.customMiddlewares, middlewares...)
	}
}

// WithChannelBuffer 调整底层传输通道缓冲区大小。
func WithChannelBuffer(size int) Option {
	return func(m *Manager) {
		if size > 0 {
			m.cfg.BufferSize = size
		}
	}
}

// NewManager 初始化并构建基于 Watermill CQRS 的事件总线管理器。
func NewManager(cfg config.EventBusConfig, log *slog.Logger, opts ...Option) (*Manager, error) {
	base := cmp.Or(log, logger.Nop())

	minLevel := slog.LevelWarn
	if cfg.LogLevel != "" {
		minLevel = logger.ParseSlogLevel(cfg.LogLevel)
	}
	adapter := NewLoggerAdapter(base, minLevel)

	m := &Manager{
		cfg:                  cfg,
		baseLogger:           cmp.Or(log, logger.Default()),
		closeTimeout:         10 * time.Second,
		retryMax:             3,
		retryInitialInterval: 100 * time.Millisecond,
		retryMaxInterval:     1 * time.Second,
		handlerTimeout:       15 * time.Second,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}

	switch m.cfg.Driver {
	case DriverGoChannel, "":
	default:
		return nil, fmt.Errorf("unsupported event bus driver: %s", m.cfg.Driver)
	}

	buffer := m.cfg.BufferSize
	if buffer <= 0 {
		buffer = 2048
	}
	pubSub := gochannel.NewGoChannel(gochannel.Config{
		OutputChannelBuffer: int64(buffer),
	}, adapter)
	m.pubSub = pubSub

	router, err := message.NewRouter(message.RouterConfig{
		CloseTimeout: m.closeTimeout,
	}, adapter)
	if err != nil {
		_ = pubSub.Close()
		return nil, fmt.Errorf("failed to create router: %w", err)
	}
	m.router = router

	retryMiddleware := middleware.Retry{
		MaxRetries:          m.retryMax,
		InitialInterval:     m.retryInitialInterval,
		MaxInterval:         m.retryMaxInterval,
		Multiplier:          2.0,
		Logger:              adapter,
		ResetContextOnRetry: true,
	}

	router.AddMiddleware(
		middleware.Recoverer,
		observabilityContextMiddleware(m.baseLogger),
		middleware.CorrelationID,
		finalErrorBoundaryMiddleware(),
		retryMiddleware.Middleware,
		middleware.Timeout(m.handlerTimeout),
	)

	for _, mw := range m.customMiddlewares {
		if mw != nil {
			router.AddMiddleware(mw)
		}
	}

	marshaler := cqrs.JSONMarshaler{
		GenerateName: func(v any) string {
			if ev, ok := v.(Event); ok {
				return ev.EventName()
			}
			return cqrs.StructName(v)
		},
	}

	eventBus, err := cqrs.NewEventBusWithConfig(pubSub, cqrs.EventBusConfig{
		GeneratePublishTopic: func(params cqrs.GenerateEventPublishTopicParams) (string, error) {
			return qualifyTopic(m.cfg.TopicPrefix, params.EventName), nil
		},
		OnPublish: injectEventMetadata,
		Marshaler: marshaler,
		Logger:    adapter,
	})
	if err != nil {
		_ = router.Close()
		_ = pubSub.Close()
		return nil, fmt.Errorf("failed to create event bus: %w", err)
	}
	m.eventBus = eventBus

	eventProcessor, err := cqrs.NewEventProcessorWithConfig(router, cqrs.EventProcessorConfig{
		GenerateSubscribeTopic: func(params cqrs.EventProcessorGenerateSubscribeTopicParams) (string, error) {
			return qualifyTopic(m.cfg.TopicPrefix, params.EventName), nil
		},
		SubscriberConstructor: func(cqrs.EventProcessorSubscriberConstructorParams) (message.Subscriber, error) {
			return pubSub, nil
		},
		Marshaler: marshaler,
		Logger:    adapter,
	})
	if err != nil {
		_ = router.Close()
		_ = pubSub.Close()
		return nil, fmt.Errorf("failed to create event processor: %w", err)
	}
	m.eventProcessor = eventProcessor

	return m, nil
}

// RegisterHandler 向 EventProcessor 注册一个或多个强类型领域事件处理器。
func (m *Manager) RegisterHandler(handlers ...EventHandler) error {
	if m == nil || m.eventProcessor == nil {
		return errors.New("event processor is not initialized")
	}
	if m.closed.Load() {
		return errors.New("event bus manager is already closed")
	}
	if len(handlers) == 0 {
		return nil
	}

	cqrsHandlers := make([]cqrs.EventHandler, 0, len(handlers))
	for _, h := range handlers {
		if h != nil {
			cqrsHandlers = append(cqrsHandlers, h)
		}
	}
	if len(cqrsHandlers) == 0 {
		return nil
	}

	return m.eventProcessor.AddHandlers(cqrsHandlers...)
}

// Publish 将强类型领域事件通过 EventBus 发布至对应的 Topic。
func (m *Manager) Publish(ctx context.Context, event Event) error {
	if m == nil || m.eventBus == nil {
		return errors.New("event bus is not initialized")
	}
	if m.closed.Load() {
		return errors.New("event bus manager is already closed")
	}
	if event == nil {
		return errors.New("event payload cannot be nil")
	}
	return m.eventBus.Publish(ctx, event)
}

// Run 启动消息路由器并开始消费事件。此方法为阻塞调用，直到上下文取消或出现错误。
func (m *Manager) Run(ctx context.Context) error {
	if m == nil || m.router == nil {
		return errors.New("event bus router is not initialized")
	}
	if m.closed.Load() {
		return errors.New("event bus manager is already closed")
	}

	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("event bus manager is already running")
	}
	m.running = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()

	return m.router.Run(ctx)
}

// Running 返回用于等待路由器就绪并开始消费的通知通道。
func (m *Manager) Running() <-chan struct{} {
	if m == nil || m.router == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return m.router.Running()
}

// Close 优雅停止消息路由器并释放底层 PubSub 资源。
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	if !m.closed.CompareAndSwap(false, true) {
		return nil
	}

	var closeErrs []error
	if m.router != nil {
		closeErrs = append(closeErrs, m.router.Close())
	}
	if m.pubSub != nil {
		closeErrs = append(closeErrs, m.pubSub.Close())
	}

	return errors.Join(closeErrs...)
}

// Shutdown 确保应用停止时自动回收总线资源，满足 do.ShutdownerWithError 契约。
func (m *Manager) Shutdown() error {
	return m.Close()
}

func qualifyTopic(prefix, eventName string) string {
	if prefix == "" {
		return eventName
	}
	return prefix + "." + eventName
}
