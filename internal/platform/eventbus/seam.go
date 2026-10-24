package eventbus

import (
	"context"
)

// Event 是领域事件的统一契约接口。
type Event interface {
	EventName() string
}

// Publisher 是业务层发布领域事件的统一契约接口。
type Publisher interface {
	// Publish 立即将领域事件发布至总线。
	Publish(ctx context.Context, event any) error
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
	// Shutdown 语义化终止事件总线，等同于 Close。
	Shutdown() error
}

// Bus 聚合发布、订阅与生命周期控制，是事件总线基础设施的完整契约。
type Bus interface {
	Publisher
	Subscriber
	Runner
}
