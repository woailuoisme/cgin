// Package eventbus 提供事件总线基础设施的工厂函数与统一构造入口。
package eventbus

import (
	"go.uber.org/zap"

	"bgin/internal/config"
)

// RegisterEventBus 依据全局配置与日志器实例化事件总线管理器。
func RegisterEventBus(cfg *config.Config, log *zap.Logger, opts ...Option) (*Manager, error) {
	if cfg == nil {
		return NewManager(config.EventBusConfig{
			Driver:      DriverGoChannel,
			TopicPrefix: "bgin",
			LogLevel:    "warn",
			BufferSize:  2048,
		}, log, opts...)
	}
	return NewManager(cfg.EventBus, log, opts...)
}
