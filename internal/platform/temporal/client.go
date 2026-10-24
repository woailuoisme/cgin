// Package temporal 提供 Temporal 分布式工作流客户端、Worker 进程管理与健康探针适配器。
package temporal

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	temporalLog "go.temporal.io/sdk/log"
	"go.uber.org/zap"

	"bgin/internal/config"
)

// NewClient 构造并连接至 Temporal 集群客户端。
func NewClient(cfg config.TemporalConfig, zapLogger *zap.Logger, otelEnabled bool) (client.Client, error) {
	var interceptors []interceptor.ClientInterceptor
	if otelEnabled {
		tracingInterceptor, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{})
		if err == nil && tracingInterceptor != nil {
			interceptors = append(interceptors, tracingInterceptor)
		}
	}

	var tLog temporalLog.Logger
	if zapLogger != nil {
		tLog = NewZapAdapter(zapLogger)
	}

	c, err := client.Dial(client.Options{
		HostPort:     cfg.HostPort,
		Namespace:    cfg.Namespace,
		Logger:       tLog,
		Interceptors: interceptors,
	})
	if err != nil {
		return nil, fmt.Errorf("temporal: dial %s failed: %w", cfg.HostPort, err)
	}
	return c, nil
}

// Ping 执行 Temporal 客户端健康探针检查。
func Ping(ctx context.Context, c client.Client) error {
	if c == nil {
		return errors.New("temporal: client is nil")
	}
	if _, err := c.CheckHealth(ctx, &client.CheckHealthRequest{}); err != nil {
		return fmt.Errorf("temporal: health check failed: %w", err)
	}
	return nil
}

// Pinger 封装 Temporal 客户端的就绪探针契约。
type Pinger struct {
	client  client.Client
	enabled bool
}

// NewPinger 创建 Temporal 健康检查探针。
func NewPinger(c client.Client, enabled bool) *Pinger {
	return &Pinger{
		client:  c,
		enabled: enabled,
	}
}

// Ping 执行上下文超时的 Temporal 服务连通性检查。
func (p *Pinger) Ping(ctx context.Context) error {
	if !p.enabled {
		return nil
	}
	return Ping(ctx, p.client)
}

// Enabled 报告 Temporal 客户端是否处于启用状态。
func (p *Pinger) Enabled() bool {
	return p.enabled
}

// Driver 返回底层驱动标识。
func (p *Pinger) Driver() string {
	return "temporal"
}
