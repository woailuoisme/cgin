// Package eventbus 提供基于 Watermill CQRS 的领域事件总线、路由器与中间件生命周期管理。
package eventbus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"bgin/internal/config"
)

var (
	_ Bus        = (*Manager)(nil)
	_ Publisher  = (*Manager)(nil)
	_ Subscriber = (*Manager)(nil)
	_ Runner     = (*Manager)(nil)
)

// Option 为 Manager 构造提供函数式配置选项。
type Option func(*Manager)

// Manager 封装基于 Watermill CQRS 的事件总线、路由器与事件处理器生命周期。
type Manager struct {
	cfg            config.EventBusConfig
	logger         watermill.LoggerAdapter
	driver         PubSubDriver
	router         *message.Router
	eventBus       *cqrs.EventBus
	eventProcessor *cqrs.EventProcessor

	closeTimeout         time.Duration
	retryMax             int
	retryInitialInterval time.Duration
	retryMaxInterval     time.Duration
	handlerTimeout       time.Duration
	customMiddlewares    []message.HandlerMiddleware

	mu      sync.RWMutex
	running bool
	closed  bool
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
func NewManager(cfg config.EventBusConfig, zlog *zap.Logger, opts ...Option) (*Manager, error) {
	zlog = cmp.Or(zlog, zap.NewNop())

	// 事件总线子系统支持独立日志级别，默认降级为 warn 以减少启动期内部流水输出。
	if cfg.LogLevel != "" {
		var lvl zapcore.Level
		if err := lvl.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
			lvl = zapcore.WarnLevel
		}
		if newCore, err := zapcore.NewIncreaseLevelCore(zlog.Core(), lvl); err == nil {
			zlog = zlog.WithOptions(zap.WrapCore(func(zapcore.Core) zapcore.Core {
				return newCore
			}))
		}
	}

	logger := NewZapLoggerAdapter(zlog)

	m := &Manager{
		cfg:                  cfg,
		logger:               logger,
		closeTimeout:         10 * time.Second,
		retryMax:             3,
		retryInitialInterval: 100 * time.Millisecond,
		retryMaxInterval:     1 * time.Second,
		handlerTimeout:       15 * time.Second,
	}

	for _, opt := range opts {
		opt(m)
	}

	driver, err := createDriver(m.cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create pubsub driver: %w", err)
	}
	m.driver = driver

	router, err := message.NewRouter(message.RouterConfig{
		CloseTimeout: m.closeTimeout,
	}, logger)
	if err != nil {
		_ = driver.Close()
		return nil, fmt.Errorf("failed to create router: %w", err)
	}
	m.router = router

	// 挂载中间件管道：异常恢复、链路追踪与上下文还原、关联标识、重试机制、超时控制与自定义拦截器。
	retryMiddleware := middleware.Retry{
		MaxRetries:          m.retryMax,
		InitialInterval:     m.retryInitialInterval,
		MaxInterval:         m.retryMaxInterval,
		Multiplier:          2.0,
		Logger:              logger,
		ResetContextOnRetry: true,
	}

	router.AddMiddleware(
		middleware.Recoverer,
		traceAndCorrelationContextMiddleware,
		middleware.CorrelationID,
		retryMiddleware.Middleware,
		middleware.Timeout(m.handlerTimeout),
	)

	for _, mw := range m.customMiddlewares {
		router.AddMiddleware(mw)
	}

	marshaler := defaultJSONMarshaler()

	eventBus, err := cqrs.NewEventBusWithConfig(driver, cqrs.EventBusConfig{
		GeneratePublishTopic: func(params cqrs.GenerateEventPublishTopicParams) (string, error) {
			return qualifyTopic(cfg.TopicPrefix, params.EventName), nil
		},
		OnPublish: injectEventMetadata,
		Marshaler: marshaler,
		Logger:    logger,
	})
	if err != nil {
		_ = router.Close()
		_ = driver.Close()
		return nil, fmt.Errorf("failed to create event bus: %w", err)
	}
	m.eventBus = eventBus

	eventProcessor, err := cqrs.NewEventProcessorWithConfig(router, cqrs.EventProcessorConfig{
		GenerateSubscribeTopic: func(params cqrs.EventProcessorGenerateSubscribeTopicParams) (string, error) {
			return qualifyTopic(cfg.TopicPrefix, params.EventName), nil
		},
		SubscriberConstructor: func(params cqrs.EventProcessorSubscriberConstructorParams) (message.Subscriber, error) {
			return driver, nil
		},
		Marshaler: marshaler,
		Logger:    logger,
	})
	if err != nil {
		_ = router.Close()
		_ = driver.Close()
		return nil, fmt.Errorf("failed to create event processor: %w", err)
	}
	m.eventProcessor = eventProcessor

	return m, nil
}

// NewTestManager 创建一个基于内存 GoChannel 的轻量测试事件管理器实例。
func NewTestManager(opts ...Option) *Manager {
	mgr, err := NewManager(config.EventBusConfig{
		Driver:      DriverGoChannel,
		TopicPrefix: "test",
		LogLevel:    "error",
		BufferSize:  1024,
	}, zap.NewNop(), opts...)
	if err != nil {
		panic(fmt.Sprintf("failed to create test event bus manager: %v", err))
	}
	return mgr
}

// EventBus 返回内部的 CQRS EventBus 实例。
func (m *Manager) EventBus() *cqrs.EventBus {
	if m == nil {
		return nil
	}
	return m.eventBus
}

// EventProcessor 返回内部的 CQRS EventProcessor 实例。
func (m *Manager) EventProcessor() *cqrs.EventProcessor {
	if m == nil {
		return nil
	}
	return m.eventProcessor
}

// Router 返回内部维护的 Watermill 消息路由器。
func (m *Manager) Router() *message.Router {
	if m == nil {
		return nil
	}
	return m.router
}

// Publisher 返回底层消息发布者。
func (m *Manager) Publisher() message.Publisher {
	if m == nil {
		return nil
	}
	return m.driver
}

// Subscriber 返回底层消息订阅者。
func (m *Manager) Subscriber() message.Subscriber {
	if m == nil {
		return nil
	}
	return m.driver
}

// RegisterHandler 向 EventProcessor 注册一个或多个强类型领域事件处理器，满足 Subscriber 接口契约。
func (m *Manager) RegisterHandler(handlers ...EventHandler) error {
	if m == nil || m.eventProcessor == nil {
		return errors.New("event processor is not initialized")
	}
	if len(handlers) == 0 {
		return nil
	}

	cqrsHandlers := lo.FilterMap(handlers, func(h EventHandler, _ int) (cqrs.EventHandler, bool) {
		return h, h != nil
	})
	if len(cqrsHandlers) == 0 {
		return nil
	}

	return m.eventProcessor.AddHandlers(cqrsHandlers...)
}

// RegisterHandlers 是 RegisterHandler 的语义化别名，方便批量注册强类型处理器。
func (m *Manager) RegisterHandlers(handlers ...EventHandler) error {
	return m.RegisterHandler(handlers...)
}

// Publish 将领域事件通过 EventBus 发布至对应的 Topic。
func (m *Manager) Publish(ctx context.Context, event any) error {
	if m == nil || m.eventBus == nil {
		return errors.New("event bus is not initialized")
	}
	return m.eventBus.Publish(ctx, event)
}

// Run 启动消息路由器并开始消费事件。此方法为阻塞调用，直到上下文取消或出现错误。
func (m *Manager) Run(ctx context.Context) error {
	if m == nil || m.router == nil {
		return errors.New("event bus router is not initialized")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("event bus manager is already closed")
	}
	if m.running {
		m.mu.Unlock()
		return errors.New("event bus manager is already running")
	}
	m.running = true
	m.mu.Unlock()

	// 复位运行标记，保证退出后实例可安全重启。
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
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()

	var errs []error
	if m.router != nil {
		errs = append(errs, m.router.Close())
	}
	if m.driver != nil {
		errs = append(errs, m.driver.Close())
	}

	return errors.Join(errs...)
}

// Shutdown 确保应用停止时自动回收总线资源，等同于 Close。
func (m *Manager) Shutdown() error {
	return m.Close()
}

func defaultJSONMarshaler() cqrs.JSONMarshaler {
	return cqrs.JSONMarshaler{
		GenerateName: func(v any) string {
			if n, ok := v.(Event); ok {
				return n.EventName()
			}
			if n, ok := v.(interface{ Name() string }); ok {
				return n.Name()
			}
			return cqrs.StructName(v)
		},
	}
}

func qualifyTopic(prefix, eventName string) string {
	if prefix == "" {
		return eventName
	}
	return prefix + "." + eventName
}
