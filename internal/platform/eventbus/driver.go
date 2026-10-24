package eventbus

import (
	"fmt"
	"sync"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"

	"bgin/internal/config"
)

// Driver 代表支持的消息传输后端驱动类型。
const (
	DriverGoChannel = "go_channel"
)

// PubSubDriver 抽象消息发布者与订阅者的统一底层驱动契约。
type PubSubDriver interface {
	message.Publisher
	message.Subscriber
	Close() error
}

// DriverFactory 是根据配置与日志记录器构造底层传输后端的工厂函数契约。
type DriverFactory func(cfg config.EventBusConfig, logger watermill.LoggerAdapter) (PubSubDriver, error)

var (
	driverMu sync.RWMutex
	drivers  = map[string]DriverFactory{
		DriverGoChannel: createGoChannelDriver,
		"":              createGoChannelDriver,
	}
)

// RegisterDriver 注册新的事件总线传输驱动工厂，实现 OCP 开放-封闭架构扩展。
func RegisterDriver(name string, factory DriverFactory) {
	driverMu.Lock()
	defer driverMu.Unlock()
	drivers[name] = factory
}

// createDriver 根据配置初始化对应的传输后端驱动。
func createDriver(cfg config.EventBusConfig, logger watermill.LoggerAdapter) (PubSubDriver, error) {
	driverMu.RLock()
	factory, exists := drivers[cfg.Driver]
	driverMu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unsupported event bus driver: %s", cfg.Driver)
	}
	return factory(cfg, logger)
}

func createGoChannelDriver(cfg config.EventBusConfig, logger watermill.LoggerAdapter) (PubSubDriver, error) {
	buffer := cfg.BufferSize
	if buffer <= 0 {
		buffer = 2048
	}

	// 采用非阻塞发布与高容量缓冲通道，避免发布端因并发消费或调度延迟而阻塞。
	pubSub := gochannel.NewGoChannel(gochannel.Config{
		OutputChannelBuffer:            int64(buffer),
		BlockPublishUntilSubscriberAck: false,
	}, logger)

	return pubSub, nil
}
