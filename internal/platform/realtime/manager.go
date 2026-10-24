// Package realtime 提供基于 Centrifugo 的高可用实时通信基础设施驱动。
package realtime

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/centrifugal/gocent/v3"
	"github.com/samber/oops"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/resilience"
)

// Config 为 config.CentrifugoConfig 的类型别名，消除重复定义并直接复用统一配置结构。
type Config = config.CentrifugoConfig

// Option 定义 Manager 配置选项函数。
type Option func(*ManagerImpl)

// WithRegistry 注入频道策略注册表。
func WithRegistry(reg *Registry) Option {
	return func(m *ManagerImpl) {
		if reg != nil {
			m.registry = reg
		}
	}
}

// ManagerImpl 实现 Centrifugo 实时通信 Manager 接口及各原子角色接口。
type ManagerImpl struct {
	cfg      *Config
	client   *gocent.Client
	registry *Registry
}

var (
	_ Manager         = (*ManagerImpl)(nil)
	_ RealtimeGateway = (*ManagerImpl)(nil)
)

// NewManager 创建 Centrifugo 实时通信管理器实例。
func NewManager(cfg *Config, opts ...Option) *ManagerImpl {
	m := &ManagerImpl{
		cfg: cfg,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}

	m.registry = cmp.Or(m.registry, NewRegistry())

	if cfg != nil && cfg.Enabled && cfg.URL != "" {
		m.client = gocent.New(gocent.Config{
			Addr: cfg.URL,
			Key:  cfg.APIKey,
		})
	}

	return m
}

// Config 返回当前实时模块基础设施配置。
func (m *ManagerImpl) Config() *Config {
	return m.cfg
}

// Client 导出底层客户端连接指针。
func (m *ManagerImpl) Client() *Client {
	return m.client
}

// Registry 返回频道策略注册表。
func (m *ManagerImpl) Registry() *Registry {
	m.registry = cmp.Or(m.registry, NewRegistry())
	return m.registry
}

// GetNodeID 返回 Centrifugo 节点标识。
func (m *ManagerImpl) GetNodeID() string {
	return "centrifugo"
}

// Driver 返回当前实时通信驱动名称。
func (m *ManagerImpl) Driver() string {
	return "centrifugo"
}

// disabled 判断实时网关是否不可用（未配置、未启用或客户端未建立）。
func (m *ManagerImpl) disabled() bool {
	return m.cfg == nil || !m.cfg.Enabled || m.client == nil
}

// Enabled 报告 Centrifugo 实时服务是否已配置并启用。
func (m *ManagerImpl) Enabled() bool {
	return !m.disabled()
}

// Ping 探测 Centrifugo 服务节点的连通性与可用性（满足 ready.Pinger 契约）。
func (m *ManagerImpl) Ping(ctx context.Context) error {
	if m.disabled() {
		return errors.New("realtime: centrifugo client is disabled or not configured")
	}
	_, err := m.client.Info(ctx)
	if err != nil {
		return oops.Code(errs.CodeInternal).In("ManagerImpl.Ping").Wrap(err)
	}
	return nil
}

var realtimeExecutor = resilience.NewNetworkExecutor(3, 50*time.Millisecond, 2*time.Second)

// retryCall 以统一重试策略（3 次、50ms 起指数退避）执行 Centrifugo 出站调用。
func retryCall(ctx context.Context, fn func() error) error {
	return resilience.Run(ctx, realtimeExecutor, fn)
}

// DispatchContext 向单个频道推送事件消息（受调用方 Context 生命周期控制）。
func (m *ManagerImpl) DispatchContext(ctx context.Context, channelName, event string, data any) error {
	if m.disabled() {
		return nil
	}

	if channelName == "" || event == "" {
		return oops.
			Code("BAD_REQUEST").
			In("ManagerImpl.Dispatch").
			Errorf("missing channel or event")
	}

	payload, err := marshalEventMessage("Dispatch", event, data)
	if err != nil {
		return err
	}

	return m.PublishRaw(ctx, channelName, payload)
}

// BroadcastContext 批量向多个频道推送同一事件消息（受 Context 控制）。
func (m *ManagerImpl) BroadcastContext(ctx context.Context, channels []string, event string, data any) error {
	if m.disabled() || len(channels) == 0 {
		return nil
	}

	if event == "" {
		return oops.
			Code("BAD_REQUEST").
			In("ManagerImpl.Broadcast").
			Errorf("missing event")
	}

	payload, err := marshalEventMessage("Broadcast", event, data)
	if err != nil {
		return err
	}

	return m.BroadcastRaw(ctx, channels, payload)
}

func marshalEventMessage(op, event string, data any) ([]byte, error) {
	payload, err := json.Marshal(EventMessage{
		Event: event,
		Data:  data,
	})
	if err != nil {
		return nil, oops.
			Code("INTERNAL_SERVER_ERROR").
			In("ManagerImpl." + op + ".Marshal").
			Wrap(err)
	}
	return payload, nil
}

// PublishRaw 向单个频道直接发布原始字节数据（规避二次 JSON 序列化开销）。
func (m *ManagerImpl) PublishRaw(ctx context.Context, channelName string, data []byte) error {
	if m.disabled() {
		return nil
	}

	if channelName == "" {
		return oops.
			Code("BAD_REQUEST").
			In("ManagerImpl.PublishRaw").
			Errorf("missing channel")
	}

	publishErr := retryCall(ctx, func() error {
		_, err := m.client.Publish(ctx, channelName, data)
		return err
	})
	if publishErr != nil {
		return oops.
			Code("INTERNAL_SERVER_ERROR").
			In("ManagerImpl.PublishRaw.Publish").
			With("channel", channelName).
			Wrap(publishErr)
	}

	return nil
}

// BroadcastRaw 批量向多个频道直接发布原始字节数据。
func (m *ManagerImpl) BroadcastRaw(ctx context.Context, channels []string, data []byte) error {
	if m.disabled() || len(channels) == 0 {
		return nil
	}

	publishErr := retryCall(ctx, func() error {
		_, err := m.client.Broadcast(ctx, channels, data)
		return err
	})
	if publishErr != nil {
		return oops.
			Code("INTERNAL_SERVER_ERROR").
			In("ManagerImpl.BroadcastRaw.Execute").
			Wrap(publishErr)
	}

	return nil
}

// Disconnect 断开指定用户的 Centrifugo 活跃连接。
func (m *ManagerImpl) Disconnect(ctx context.Context, userID string) error {
	if m.disabled() || userID == "" {
		return nil
	}

	return retryCall(ctx, func() error {
		return m.client.Disconnect(ctx, userID)
	})
}

// Unsubscribe 取消指定用户在特定频道的订阅。
func (m *ManagerImpl) Unsubscribe(ctx context.Context, userID, channel string) error {
	if m.disabled() || userID == "" || channel == "" {
		return nil
	}

	return retryCall(ctx, func() error {
		return m.client.Unsubscribe(ctx, channel, userID)
	})
}

// History 查询指定频道的历史消息记录。
func (m *ManagerImpl) History(ctx context.Context, channel string, limit int) ([]Publication, error) {
	if m.disabled() || channel == "" {
		return nil, nil
	}

	var opts []gocent.HistoryOption
	if limit > 0 {
		opts = append(opts, gocent.WithLimit(limit))
	}

	var res gocent.HistoryResult
	err := retryCall(ctx, func() error {
		var hErr error
		res, hErr = m.client.History(ctx, channel, opts...)
		return hErr
	})
	if err != nil {
		return nil, oops.
			Code("INTERNAL_SERVER_ERROR").
			In("ManagerImpl.History").
			With("channel", channel).
			Wrap(err)
	}

	return res.Publications, nil
}

// Presence 查询指定频道的在线成员信息列表。
func (m *ManagerImpl) Presence(ctx context.Context, channel string) (map[string]ClientInfo, error) {
	if m.disabled() || channel == "" {
		return make(map[string]ClientInfo), nil
	}

	var res gocent.PresenceResult
	err := retryCall(ctx, func() error {
		var pErr error
		res, pErr = m.client.Presence(ctx, channel)
		return pErr
	})
	if err != nil {
		return nil, oops.
			Code("INTERNAL_SERVER_ERROR").
			In("ManagerImpl.Presence").
			With("channel", channel).
			Wrap(err)
	}

	return res.Presence, nil
}

// PresenceStats 查询指定频道的成员在线统计数据（客户端数与独立用户数）。
func (m *ManagerImpl) PresenceStats(ctx context.Context, channel string) (PresenceStatsResult, error) {
	if m.disabled() || channel == "" {
		return PresenceStatsResult{}, nil
	}

	var res gocent.PresenceStatsResult
	err := retryCall(ctx, func() error {
		var sErr error
		res, sErr = m.client.PresenceStats(ctx, channel)
		return sErr
	})
	if err != nil {
		return PresenceStatsResult{}, oops.
			Code("INTERNAL_SERVER_ERROR").
			In("ManagerImpl.PresenceStats").
			With("channel", channel).
			Wrap(err)
	}

	return res, nil
}

// GenerateConnectionToken 为指定用户签发 Centrifugo 客户端连接凭证。
func (m *ManagerImpl) GenerateConnectionToken(
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

// GenerateSubscriptionToken 为特定频道签发客户端订阅凭证（支持免 Webhook 高性能直连）。
func (m *ManagerImpl) GenerateSubscriptionToken(
	userID, channel string,
	expireAt time.Time,
	opts ...SubscriptionTokenOption,
) (string, error) {
	if m.cfg == nil {
		return "", errors.New("realtime config is nil")
	}
	return GenerateSubscriptionToken(m.cfg.Secret, userID, channel, expireAt, opts...)
}

// Token 快捷签发用于客户端认证的连接凭证（GenerateConnectionToken 的便捷别名）。
func (m *ManagerImpl) Token(
	userID string,
	expireAt time.Time,
	info map[string]any,
	meta map[string]any,
	channels ...string,
) (string, error) {
	return m.GenerateConnectionToken(userID, expireAt, info, meta, channels...)
}
