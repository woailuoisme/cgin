// Package realtime 包含 Centrifugo 实时通信辅助工厂与错误定义。
package realtime

import (
	"errors"

	"bgin/internal/config"
)

// ErrRealtimeDisabled 表示实时通信服务未在配置中启用。
var ErrRealtimeDisabled = errors.New("realtime service is disabled")

// RegisterRealtimeGateway 实例化并注册实时通信网关。
func RegisterRealtimeGateway(cfg *config.Config, reg *Registry) (Gateway, error) {
	if cfg == nil {
		return NewManager(nil, WithRegistry(reg)), nil
	}
	return NewManager(&cfg.Realtime, WithRegistry(reg)), nil
}

// RegisterRealtime 实例化并注册实时通信管理器。
func RegisterRealtime(cfg *config.Config) (Manager, error) {
	if cfg == nil || !cfg.Realtime.Enabled {
		return nil, ErrRealtimeDisabled
	}
	return NewManager(&cfg.Realtime), nil
}
