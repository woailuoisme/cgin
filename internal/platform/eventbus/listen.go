package eventbus

import (
	"context"

	"github.com/ThreeDotsLabs/watermill/components/cqrs"
)

// SubscriptionOption 为事件监听器提供可扩展的函数式配置选项。
type SubscriptionOption func(*subscriptionConfig)

type subscriptionConfig struct {
	// 预留单 Handler 高级选项扩展位（如独立并发 Worker、隔离重试等）
}

// Listen 使用 Go 泛型向事件总线声明式注册强类型领域事件监听器。
// 底层自动封装适配 EventHandler 契约，保证编译期类型安全与零业务样板代码。
func Listen[T any](bus Subscriber, name string, handler func(ctx context.Context, event *T) error, opts ...SubscriptionOption) error {
	var cfg subscriptionConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	h := cqrs.NewEventHandler(name, handler)
	return bus.RegisterHandler(h)
}
