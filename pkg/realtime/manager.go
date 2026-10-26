// Package realtime 提供基于 Centrifugo 的高可用实时通信基础设施驱动。
package realtime

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/centrifugal/gocent/v3"
	"github.com/samber/oops"

	"cgin/internal/config"
	"cgin/pkg/errs"
	"cgin/pkg/resilience"
)

// Config 为 config.CentrifugoConfig 的类型别名，消除重复定义并直接复用统一配置结构。
type Config = config.CentrifugoConfig

// manager 是 Gateway 的唯一实现：一个 Centrifugo HTTP API 客户端加一份频道订阅策略表。
type manager struct {
	cfg      *Config
	client   *gocent.Client
	registry *Registry
}

var _ Gateway = (*manager)(nil)

// NewManager 创建 Centrifugo 实时通信网关。
// reg 为 nil 时退化为空注册表 —— 所有频道订阅都会被拒，符合 Fail-Closed 默认。
func NewManager(cfg *Config, reg *Registry) Gateway {
	m := &manager{
		cfg:      cfg,
		registry: cmp.Or(reg, NewRegistry()),
	}

	if cfg != nil && cfg.Enabled && cfg.URL != "" {
		m.client = gocent.New(gocent.Config{
			Addr: cfg.URL,
			Key:  cfg.APIKey,
		})
	}

	return m
}

// Registry 返回频道订阅策略注册表。
func (m *manager) Registry() *Registry {
	return m.registry
}

// Driver 返回当前实时通信驱动名称。
func (m *manager) Driver() string {
	return "centrifugo"
}

// disabled 判断实时网关是否不可用（未配置、未启用或客户端未建立）。
func (m *manager) disabled() bool {
	return m.cfg == nil || !m.cfg.Enabled || m.client == nil
}

// Enabled 报告 Centrifugo 实时服务是否已配置并启用。
func (m *manager) Enabled() bool {
	return !m.disabled()
}

// Ping 探测 Centrifugo 服务节点的连通性与可用性（满足 ready.Pinger 契约）。
func (m *manager) Ping(ctx context.Context) error {
	if m.disabled() {
		return errors.New("realtime: centrifugo client is disabled or not configured")
	}
	if _, err := m.client.Info(ctx); err != nil {
		return oops.Code(errs.CodeInternal).In("manager.Ping").Wrap(err)
	}
	return nil
}

var publishExecutor = resilience.NewNetworkExecutor(3, 50*time.Millisecond, 2*time.Second)

// DispatchContext 向单个频道推送事件消息（受调用方 Context 生命周期控制）。
//
// 重试必须携带 idempotency_key：发布本身不是幂等操作，Centrifugo 靠该键丢弃重复
// 发布，否则一次网络抖动的重试就会让订阅端收到两条相同事件。
func (m *manager) DispatchContext(ctx context.Context, channel, event string, data any) error {
	if m.disabled() {
		return nil
	}

	if channel == "" || event == "" {
		return oops.
			Code(errs.CodeBadRequest).
			In("manager.Dispatch").
			Errorf("missing channel or event")
	}

	payload, err := json.Marshal(EventMessage{Event: event, Data: data})
	if err != nil {
		return oops.Code(errs.CodeInternal).In("manager.Dispatch.Marshal").Wrap(err)
	}

	key, err := newIdempotencyKey()
	if err != nil {
		return oops.Code(errs.CodeInternal).In("manager.Dispatch.IdempotencyKey").Wrap(err)
	}

	publishErr := resilience.Run(ctx, publishExecutor, func() error {
		_, pErr := m.client.Publish(ctx, channel, payload, gocent.WithIdempotencyKey(key))
		return pErr
	})
	if publishErr != nil {
		return oops.
			Code(errs.CodeInternal).
			In("manager.Dispatch.Publish").
			With("channel", channel).
			Wrap(publishErr)
	}

	return nil
}

// newIdempotencyKey 生成单次发布内的稳定去重键：重试复用同一个键，不同调用互不相同。
func newIdempotencyKey() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// GenerateConnectionToken 为指定用户签发 Centrifugo 客户端连接凭证。
func (m *manager) GenerateConnectionToken(
	userID string,
	expireAt time.Time,
	info map[string]any,
	meta map[string]any,
	channels ...string,
) (string, error) {
	if m.cfg == nil {
		return "", errors.New("realtime config is nil")
	}
	return GenerateConnectionToken(m.cfg.Secret, userID, expireAt, info, meta, channels...)
}
