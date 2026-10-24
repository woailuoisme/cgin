package payment

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/samber/lo"
	"github.com/samber/oops"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/payment/enums"
)

// DriverFactory 定义支付渠道驱动的构造工厂函数契约。
type DriverFactory func(application string, appCfg any, isProd bool, certsDir string, logger *zap.Logger) (Provider, error)

var (
	driverMu              sync.RWMutex
	customDriverFactories = make(map[enums.PaymentChannel]DriverFactory)
)

// wechatFactory 内置微信支付驱动构造函数。
func wechatFactory(application string, rawCfg any, isProd bool, certsDir string, logger *zap.Logger) (Provider, error) {
	cfg, ok := rawCfg.(config.WechatPaymentAppConfig)
	if !ok {
		return nil, oops.Code(errs.CodeInvalidPaymentConfig).Errorf("expected WechatPaymentAppConfig, got %T", rawCfg)
	}
	return newWechatProvider(application, cfg, isProd, certsDir, logger)
}

// alipayFactory 内置支付宝支付驱动构造函数。
func alipayFactory(application string, rawCfg any, isProd bool, certsDir string, logger *zap.Logger) (Provider, error) {
	cfg, ok := rawCfg.(config.AlipayPaymentAppConfig)
	if !ok {
		return nil, oops.Code(errs.CodeInvalidPaymentConfig).Errorf("expected AlipayPaymentAppConfig, got %T", rawCfg)
	}
	return newAlipayProvider(application, cfg, isProd, certsDir, logger)
}

// RegisterDriver 注册指定支付渠道的自定义驱动构造工厂（遵循开闭原则 OCP）。
func RegisterDriver(channel enums.PaymentChannel, factory DriverFactory) {
	driverMu.Lock()
	defer driverMu.Unlock()
	customDriverFactories[channel] = factory
}

// GetDriverFactory 获取已注册的渠道驱动工厂（内置渠道无需 init 注册，支持静态无副作用分发）。
func GetDriverFactory(channel enums.PaymentChannel) (DriverFactory, bool) {
	switch channel {
	case enums.PaymentChannelWechat:
		return wechatFactory, true
	case enums.PaymentChannelAlipay:
		return alipayFactory, true
	default:
		driverMu.RLock()
		defer driverMu.RUnlock()
		factory, ok := customDriverFactories[channel]
		return factory, ok
	}
}

// FakeApplicationKey 是本地开发与端到端演练下 Fake 渠道使用的应用标识。
const FakeApplicationKey = "fake"

// defaultApplicationKey 是单应用默认标识。
const defaultApplicationKey = "default"

// Manager 负责管理已注册的支付渠道应用，并根据场景路由到具体 Provider。
type Manager struct {
	mu          sync.RWMutex
	enabled     bool
	providers   map[string]Provider
	sceneOwners map[string]Provider
	order       []string
}

var _ Gateway = (*Manager)(nil)

// NewManager 依据配置构造支付管理器，并在启动期校验渠道凭据。
func NewManager(cfg *config.Config, logger *zap.Logger) (*Manager, error) {
	logger = cmp.Or(logger, zap.NewNop())
	manager := &Manager{
		providers:   make(map[string]Provider),
		sceneOwners: make(map[string]Provider),
	}
	if cfg == nil {
		return manager, nil
	}

	isProd := isProductionEnv(cfg.App.Env)
	useFake := cfg.Payment.FakeEnabled && !isProd

	if err := manager.loadWechat(cfg, logger); err != nil {
		return nil, err
	}
	if err := manager.loadAlipay(cfg, isProd, logger); err != nil {
		return nil, err
	}

	if useFake {
		manager.registerFakeProviders(logger)
	}

	// 渠道遵循「配置即启用」：注册到任一渠道应用即视为支付已启用，不存在独立开关。
	manager.enabled = len(manager.providers) > 0

	return manager, nil
}

// loadWechat 加载微信支付渠道的全部应用。
// 未配置任何应用即视为该渠道未启用：既不注册也不报错。
func (m *Manager) loadWechat(cfg *config.Config, logger *zap.Logger) error {
	applications, err := config.ResolveWechatApplications(cfg.Payment.Wechat)
	if err != nil {
		return err
	}

	factory, ok := GetDriverFactory(enums.PaymentChannelWechat)
	if !ok {
		return oops.Code(errs.CodePaymentDriverNotFound).Errorf("payment driver factory not found for wechat")
	}

	for _, application := range sortedKeys(applications) {
		provider, err := factory(application, applications[application], false, cfg.Payment.CertsDir, logger)
		if err != nil {
			return err
		}
		if err := m.register(provider); err != nil {
			return err
		}
	}

	return nil
}

// loadAlipay 加载支付宝渠道的全部应用。
// 未配置任何应用即视为该渠道未启用：既不注册也不报错。
func (m *Manager) loadAlipay(cfg *config.Config, isProd bool, logger *zap.Logger) error {
	applications, err := config.ResolveAlipayApplications(cfg.Payment.Alipay)
	if err != nil {
		return err
	}

	factory, ok := GetDriverFactory(enums.PaymentChannelAlipay)
	if !ok {
		return oops.Code(errs.CodePaymentDriverNotFound).Errorf("payment driver factory not found for alipay")
	}

	for _, application := range sortedKeys(applications) {
		provider, err := factory(application, applications[application], isProd, cfg.Payment.CertsDir, logger)
		if err != nil {
			return err
		}
		if err := m.register(provider); err != nil {
			return err
		}
	}

	return nil
}

// registerFakeProviders 在显式开启且非生产环境时注册 Fake 渠道。
// 这样本地开发与端到端演练无需真实商户号即可跑通收银台链路，且该能力永远不会进入生产环境。
func (m *Manager) registerFakeProviders(logger *zap.Logger) {
	for _, channel := range enums.PaymentChannelValues() {
		provider := NewFakeProvider(channel, FakeApplicationKey)
		if err := m.register(provider); err != nil {
			logger.Warn("failed to register fake payment provider",
				zap.Error(err),
				zap.String("channel", channel.String()),
			)
			continue
		}

		logger.Warn("payment channel is served by the in-process fake provider",
			zap.String("channel", channel.String()),
			zap.String("application", FakeApplicationKey),
		)
	}
}

// register 注册一个渠道应用，并拒绝重复声明的场景。
// 同一渠道内一个场景只能由一个应用承载，否则下单时无法确定使用哪套凭据，必须在启动期就暴露。
func (m *Manager) register(provider Provider) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := providerKey(provider.Channel(), provider.Application())
	if _, exists := m.providers[key]; exists {
		return errs.Conflict(
			fmt.Sprintf("payment application is registered twice: %s/%s", provider.Channel(), provider.Application()),
		)
	}

	claims := make(map[string]struct{}, len(provider.Scenes()))
	for _, scene := range provider.Scenes() {
		claimKey := sceneOwnerKey(provider.Channel(), scene)
		if owner, exists := m.sceneOwners[claimKey]; exists {
			return errs.Conflict(
				fmt.Sprintf("payment scene %s is claimed by more than one application (%s, %s)", scene, owner.Application(), provider.Application()),
			)
		}

		claims[claimKey] = struct{}{}
	}

	for claimKey := range claims {
		m.sceneOwners[claimKey] = provider
	}

	m.providers[key] = provider
	m.order = append(m.order, key)

	return nil
}

// Register 动态注册一个已实例化的渠道应用 Provider，满足开闭原则。
func (m *Manager) Register(provider Provider) error {
	if m == nil {
		return errs.ServiceUnavailable("payment manager is not initialized")
	}
	if err := m.register(provider); err != nil {
		return err
	}
	m.mu.Lock()
	m.enabled = len(m.providers) > 0
	m.mu.Unlock()

	return nil
}

// Enabled 返回支付模块是否启用了任一渠道。
func (m *Manager) Enabled() bool {
	if m == nil {
		return false
	}

	return m.enabled
}

// Create 发起一次收款，内部自动根据 req.Scene 路由到声明了该场景的渠道应用。
func (m *Manager) Create(ctx context.Context, req CreateRequest) (*Instruction, error) {
	if m == nil || !m.enabled {
		return nil, errs.ServiceUnavailable("payment gateway is not enabled")
	}
	if !req.Scene.IsValid() {
		return nil, errs.BadRequest("invalid payment scene: " + req.Scene.String())
	}

	provider, err := m.ForScene(req.Scene.Channel(), req.Scene)
	if err != nil {
		return nil, err
	}

	payable, ok := provider.(Payable)
	if !ok {
		return nil, oops.Code(errs.CodePaymentCapabilityNotSupported).
			With("channel", req.Scene.Channel().String()).
			With("application", provider.Application()).
			Wrap(ErrCapabilityNotSupported)
	}

	return payable.Create(ctx, req)
}

// Query 主动查询支付状态。若未显式指定 application，则自动使用对应渠道的默认应用。
func (m *Manager) Query(
	ctx context.Context,
	channel enums.PaymentChannel,
	req QueryRequest,
	application ...string,
) (*PaymentResult, error) {
	provider, err := m.resolveAppProvider(channel, application...)
	if err != nil {
		return nil, err
	}

	queryable, ok := provider.(Queryable)
	if !ok {
		return nil, oops.Code(errs.CodePaymentCapabilityNotSupported).
			With("channel", channel.String()).
			With("application", provider.Application()).
			Wrap(ErrCapabilityNotSupported)
	}

	return queryable.Query(ctx, req)
}

// Close 关闭一笔未支付的收款。若未显式指定 application，则自动使用对应渠道的默认应用。
func (m *Manager) Close(
	ctx context.Context,
	channel enums.PaymentChannel,
	req CloseRequest,
	application ...string,
) error {
	provider, err := m.resolveAppProvider(channel, application...)
	if err != nil {
		return err
	}

	payable, ok := provider.(Payable)
	if !ok {
		return oops.Code(errs.CodePaymentCapabilityNotSupported).
			With("channel", channel.String()).
			With("application", provider.Application()).
			Wrap(ErrCapabilityNotSupported)
	}

	return payable.Close(ctx, req)
}

// Refund 发起全额或部分退款。若未显式指定 application，则自动使用对应渠道的默认应用。
func (m *Manager) Refund(
	ctx context.Context,
	channel enums.PaymentChannel,
	req RefundRequest,
	application ...string,
) (*RefundResult, error) {
	provider, err := m.resolveAppProvider(channel, application...)
	if err != nil {
		return nil, err
	}

	refundable, ok := provider.(Refundable)
	if !ok {
		return nil, oops.Code(errs.CodePaymentCapabilityNotSupported).
			With("channel", channel.String()).
			With("application", provider.Application()).
			Wrap(ErrCapabilityNotSupported)
	}

	return refundable.Refund(ctx, req)
}

// QueryRefund 主动查询退款状态。若未显式指定 application，则自动使用对应渠道的默认应用。
func (m *Manager) QueryRefund(
	ctx context.Context,
	channel enums.PaymentChannel,
	req QueryRefundRequest,
	application ...string,
) (*RefundResult, error) {
	provider, err := m.resolveAppProvider(channel, application...)
	if err != nil {
		return nil, err
	}

	refundable, ok := provider.(Refundable)
	if !ok {
		return nil, oops.Code(errs.CodePaymentCapabilityNotSupported).
			With("channel", channel.String()).
			With("application", provider.Application()).
			Wrap(ErrCapabilityNotSupported)
	}

	return refundable.QueryRefund(ctx, req)
}

// ParseNotification 聚合解析并验签渠道异步通知。
func (m *Manager) ParseNotification(
	_ context.Context,
	channel enums.PaymentChannel,
	application string,
	req *http.Request,
) (*Notification, error) {
	provider, err := m.resolveAppProvider(channel, application)
	if err != nil {
		return nil, err
	}

	verifier, ok := provider.(WebhookVerifiable)
	if !ok {
		return nil, oops.Code(errs.CodePaymentCapabilityNotSupported).
			With("channel", channel.String()).
			With("application", provider.Application()).
			Wrap(ErrCapabilityNotSupported)
	}

	return verifier.ParseNotification(req)
}

// resolveAppProvider 按渠道和可选应用标识解析 Provider。
func (m *Manager) resolveAppProvider(channel enums.PaymentChannel, application ...string) (Provider, error) {
	if m == nil || !m.enabled {
		return nil, errs.ServiceUnavailable("payment gateway is not enabled")
	}
	if !channel.IsValid() {
		return nil, errs.BadRequest("invalid payment channel: " + channel.String())
	}

	if len(application) > 0 && strings.TrimSpace(application[0]) != "" {
		return m.For(channel, application[0])
	}

	apps := m.Applications(channel)
	if len(apps) == 0 {
		return nil, errs.ServiceUnavailable("no payment application configured for channel: " + channel.String())
	}

	if slices.Contains(apps, defaultApplicationKey) {
		return m.For(channel, defaultApplicationKey)
	}

	return m.For(channel, apps[0])
}

// For 按「渠道 + 应用标识」解析 Provider。
func (m *Manager) For(channel enums.PaymentChannel, application string) (Provider, error) {
	if m == nil {
		return nil, errs.ServiceUnavailable("payment manager is not initialized")
	}
	if !channel.IsValid() {
		return nil, errs.BadRequest("invalid payment channel: " + channel.String())
	}

	trimmed := strings.TrimSpace(application)
	if trimmed == "" {
		return nil, errs.BadRequest("payment application is required for channel: " + channel.String())
	}

	m.mu.RLock()
	provider, exists := m.providers[providerKey(channel, trimmed)]
	m.mu.RUnlock()
	if !exists {
		return nil, errs.ServiceUnavailable(
			fmt.Sprintf("payment application %s is not configured for channel %s", trimmed, channel.String()),
		)
	}

	return provider, nil
}

// ForScene 按「渠道 + 支付场景」解析 Provider。
func (m *Manager) ForScene(channel enums.PaymentChannel, scene enums.PaymentScene) (Provider, error) {
	if m == nil {
		return nil, errs.ServiceUnavailable("payment manager is not initialized")
	}
	if !channel.IsValid() {
		return nil, errs.BadRequest("invalid payment channel: " + channel.String())
	}
	if !scene.IsValid() {
		return nil, errs.BadRequest("invalid payment scene: " + scene.String())
	}
	if scene.Channel() != channel {
		return nil, errs.BadRequest(
			fmt.Sprintf("payment scene %s does not belong to channel %s", scene.String(), channel.String()),
		)
	}

	m.mu.RLock()
	provider, exists := m.sceneOwners[sceneOwnerKey(channel, scene)]
	m.mu.RUnlock()
	if !exists {
		return nil, errs.ServiceUnavailable(
			fmt.Sprintf("no payment application supports the requested scene %s on channel %s", scene.String(), channel.String()),
		)
	}

	return provider, nil
}

// Applications 返回指定渠道下已注册的应用标识。
func (m *Manager) Applications(channel enums.PaymentChannel) []string {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	return lo.FilterMap(m.order, func(key string, _ int) (string, bool) {
		provider := m.providers[key]
		return provider.Application(), provider.Channel() == channel
	})
}

// HealthCheck 检查全部渠道应用的健康状态。
// 未配置任何渠道应用时视为健康，避免未接入支付的部署被误判为降级。
func (m *Manager) HealthCheck() error {
	if m == nil {
		return errs.ServiceUnavailable("payment manager is not initialized")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.providers) == 0 {
		return nil
	}

	failures := make([]error, 0, len(m.order))
	for _, key := range m.order {
		if err := m.providers[key].HealthCheck(); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", key, err))
		}
	}

	return errors.Join(failures...)
}

// providerKey 拼接 Provider 的注册键。
func providerKey(channel enums.PaymentChannel, application string) string {
	return channel.String() + "/" + application
}

// sceneOwnerKey 拼接场景归属键。
func sceneOwnerKey(channel enums.PaymentChannel, scene enums.PaymentScene) string {
	return channel.String() + "/" + scene.String()
}

// isProductionEnv 判断当前是否运行在生产环境。
// 生产环境不会注册任何 Fake 渠道，支付宝也据此在正式与沙箱网关之间切换。
func isProductionEnv(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "production", "prod":
		return true
	default:
		return false
	}
}

// sortedKeys 返回映射的键并按字典序排序，保证启动期构造顺序可复现。
func sortedKeys[V any](values map[string]V) []string {
	return slices.Sorted(maps.Keys(values))
}
