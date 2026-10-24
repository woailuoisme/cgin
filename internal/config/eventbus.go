package config

import (
	"strings"
)

// EventBusConfig 描述事件总线与 CQRS 路由参数。
type EventBusConfig struct {
	Driver      string `mapstructure:"eventbus_driver"`
	TopicPrefix string `mapstructure:"eventbus_topic_prefix"`
	LogLevel    string `mapstructure:"eventbus_log_level"`
	BufferSize  int    `mapstructure:"eventbus_buffer_size"`
}

// validateEventBus 校验事件总线配置并设置合理默认值。
func (c *Config) validateEventBus() error {
	if strings.TrimSpace(c.EventBus.Driver) == "" {
		c.EventBus.Driver = "go_channel"
	}
	if strings.TrimSpace(c.EventBus.TopicPrefix) == "" {
		c.EventBus.TopicPrefix = "bgin"
	}
	if strings.TrimSpace(c.EventBus.LogLevel) == "" {
		c.EventBus.LogLevel = "warn"
	}
	if c.EventBus.BufferSize <= 0 {
		c.EventBus.BufferSize = 2048
	}
	return nil
}
