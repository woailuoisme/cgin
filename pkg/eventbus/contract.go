// Package eventbus 提供基于 Watermill CQRS 的强类型领域事件总线、路由管道与中间件生命周期管理。
package eventbus

import (
	"context"
	"fmt"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"

	"cgin/internal/config"
)

// DriverGoChannel 代表基于进程内内存通道的默认事件传输驱动。
const DriverGoChannel = "go_channel"

// Event 是领域事件的统一强类型契约接口（值接收者实现，使 T 与 *T 均满足契约）。
type Event interface {
	EventName() string
}

// Publisher 是业务层发布领域事件的统一契约接口。
type Publisher interface {
	// Publish 将强类型领域事件发布至事件总线。
	Publish(ctx context.Context, event Event) error
}

// EventHandler 定义领域事件处理器的统一契约，解耦底层具体 CQRS 库。
type EventHandler interface {
	// HandlerName 返回事件处理器的唯一标识名称。
	HandlerName() string
	// NewEvent 返回用于反序列化的事件空实例指针。
	NewEvent() any
	// Handle 执行事件的具体业务处理逻辑。
	Handle(ctx context.Context, event any) error
}

// Subscriber 是事件监听器注册与声明的契约接口。
type Subscriber interface {
	// RegisterHandler 向事件总线注册一个或多个强类型事件处理器。
	RegisterHandler(handlers ...EventHandler) error
}

// Runner 是消息路由器与总线生命周期管理契约。
type Runner interface {
	// Run 启动事件路由并开始消费，阻塞直到上下文取消或发生致命错误。
	Run(ctx context.Context) error
	// Running 返回用于等待路由器就绪的通知通道。
	Running() <-chan struct{}
	// Close 优雅停止事件路由器并释放底层 PubSub 资源。
	Close() error
	// Shutdown 语义化终止事件总线，满足 do.ShutdownerWithError 契约。
	Shutdown() error
}

// Bus 聚合发布、订阅与生命周期控制，是事件总线基础设施的完整契约。
type Bus interface {
	Publisher
	Subscriber
	Runner
}

// PubSubDriver 抽象消息发布者与订阅者的统一底层驱动契约。
type PubSubDriver interface {
	message.Publisher
	message.Subscriber
	Close() error
}

// nopPublisher 是零分配的无状态空发布器实现（Null Object 模式）。
type nopPublisher struct{}

var defaultNopPublisher Publisher = nopPublisher{}

func (nopPublisher) Publish(context.Context, Event) error { return nil }

// NopPublisher 返回静默丢弃所有事件的空发布器，作为可选 Publisher 依赖的默认兜底（对标 logger.Nop()）。
func NopPublisher() Publisher {
	return defaultNopPublisher
}

// Listen 使用 Go 泛型向事件总线声明式注册强类型领域事件监听器。
// 编译期强制要求事件类型 T 实现 Event 契约。
func Listen[T Event](bus Subscriber, name string, handler func(ctx context.Context, event *T) error) error {
	if bus == nil {
		return fmt.Errorf("eventbus: cannot register handler %q on nil subscriber", name)
	}
	return bus.RegisterHandler(cqrs.NewEventHandler(name, handler))
}

// createDriver 根据配置初始化对应的传输后端驱动。
func createDriver(cfg config.EventBusConfig, log watermill.LoggerAdapter) (PubSubDriver, error) {
	switch cfg.Driver {
	case DriverGoChannel, "":
		buffer := cfg.BufferSize
		if buffer <= 0 {
			buffer = 2048
		}
		return gochannel.NewGoChannel(gochannel.Config{
			OutputChannelBuffer: int64(buffer),
		}, log), nil
	default:
		return nil, fmt.Errorf("unsupported event bus driver: %s", cfg.Driver)
	}
}
