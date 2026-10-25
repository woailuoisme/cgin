package eventbus

import (
	"fmt"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"

	"cgin/internal/config"
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

// createDriver 根据配置初始化对应的传输后端驱动。
func createDriver(cfg config.EventBusConfig, logger watermill.LoggerAdapter) (PubSubDriver, error) {
	switch cfg.Driver {
	case DriverGoChannel, "":
		return createGoChannelDriver(cfg, logger)
	default:
		return nil, fmt.Errorf("unsupported event bus driver: %s", cfg.Driver)
	}
}

func createGoChannelDriver(cfg config.EventBusConfig, logger watermill.LoggerAdapter) (PubSubDriver, error) {
	buffer := cfg.BufferSize
	if buffer <= 0 {
		buffer = 2048
	}

	// 采用非阻塞发布与高容量缓冲通道，避免发布端因并发消费或调度延迟而阻塞。
	pubSub := gochannel.NewGoChannel(gochannel.Config{
		OutputChannelBuffer: int64(buffer),
	}, logger)

	return pubSub, nil
}
