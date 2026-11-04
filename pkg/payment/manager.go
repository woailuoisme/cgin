package payment

import (
	"cmp"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dromara/carbon/v2"
	"github.com/rs/zerolog"
	"github.com/samber/lo"
	"github.com/samber/oops"

	"cgin/internal/config"
	"cgin/pkg/errs"
	"cgin/pkg/logger"
)

// FakeApplicationKey 是本地开发与端到端演练下 Fake 渠道使用的应用标识。
const FakeApplicationKey = "fake"

// defaultApplicationKey 是单应用默认标识。
const defaultApplicationKey = "default"

const (
	// channelTimeLayout 兼容部分渠道返回的「空格分隔」时间格式。
	channelTimeLayout = "2006-01-02 15:04:05"
	// maxChannelErrorLength 限制渠道错误原文进入日志的长度，避免超大报文冲垮日志。
	maxChannelErrorLength = 512
	// paymentLogPlaceholder 是 SDK 传入空日志参数时的占位消息。
	paymentLogPlaceholder = "gopay sdk log entry"
)

var channelTimeLayouts = []string{time.RFC3339, channelTimeLayout}

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
func NewManager(cfg config.PaymentConfig, log *zerolog.Logger) (*Manager, error) {
	log = cmp.Or(log, logger.Nop())
	manager := &Manager{
		providers:   make(map[string]Provider),
		sceneOwners: make(map[string]Provider),
	}

	if err := manager.loadWechat(cfg, log); err != nil {
		return nil, err
	}
	if err := manager.loadAlipay(cfg, log); err != nil {
		return nil, err
	}

	if cfg.FakeEnabled && !cfg.Production {
		manager.registerFakeProviders(log)
	}

	// 渠道遵循「配置即启用」：注册到任一渠道应用即视为支付已启用，不存在独立开关。
	manager.enabled = len(manager.providers) > 0

	return manager, nil
}

// loadWechat 加载微信支付渠道的全部应用。
// 未配置任何应用即视为该渠道未启用：既不注册也不报错。
func (m *Manager) loadWechat(cfg config.PaymentConfig, log *zerolog.Logger) error {
	applications, err := cfg.Wechat.Applications()
	if err != nil {
		return err
	}

	for _, application := range sortedKeys(applications) {
		provider, err := newWechatProvider(application, applications[application], false, cfg.CertsDir, log)
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
func (m *Manager) loadAlipay(cfg config.PaymentConfig, log *zerolog.Logger) error {
	applications, err := cfg.Alipay.Applications()
	if err != nil {
		return err
	}

	for _, application := range sortedKeys(applications) {
		provider, err := newAlipayProvider(application, applications[application], cfg.Production, cfg.CertsDir, log)
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
func (m *Manager) registerFakeProviders(log *zerolog.Logger) {
	for _, channel := range ChannelValues() {
		provider := NewFakeProvider(channel, FakeApplicationKey)
		if err := m.register(provider); err != nil {
			log.Warn().
				Err(err).
				Str("channel", channel.String()).
				Msg("failed to register fake payment provider")
			continue
		}

		log.Warn().
			Str("channel", channel.String()).
			Str("application", FakeApplicationKey).
			Msg("payment channel is served by the in-process fake provider")
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
	return m.isEnabled()
}

// isEnabled 在锁内读取启用状态，与 Register 的写入共用同一把锁。
func (m *Manager) isEnabled() bool {
	if m == nil {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabled
}

// capability 取出 provider 的指定能力；该渠道应用未实现时返回统一的「能力不支持」错误。
func capability[T any](provider Provider, channel Channel) (T, error) {
	impl, ok := any(provider).(T)
	if ok {
		return impl, nil
	}

	var zero T
	return zero, oops.Code(errs.CodePaymentCapabilityNotSupported).
		With("channel", channel.String()).
		With("application", provider.Application()).
		Wrap(ErrCapabilityNotSupported)
}

// Create 发起一次收款，内部自动根据 req.Scene 路由到声明了该场景的渠道应用。
func (m *Manager) Create(ctx context.Context, req CreateRequest) (*Instruction, error) {
	if !m.isEnabled() {
		return nil, errs.ServiceUnavailable("payment gateway is not enabled")
	}
	if !req.Scene.IsValid() {
		return nil, errs.BadRequest("invalid payment scene: " + req.Scene.String())
	}

	provider, err := m.ForScene(req.Scene.Channel(), req.Scene)
	if err != nil {
		return nil, err
	}

	payable, err := capability[Payable](provider, req.Scene.Channel())
	if err != nil {
		return nil, err
	}

	return payable.Create(ctx, req)
}

// Query 主动查询支付状态。若未显式指定 application，则自动使用对应渠道的默认应用。
func (m *Manager) Query(
	ctx context.Context,
	channel Channel,
	req QueryRequest,
	application ...string,
) (*PaymentResult, error) {
	provider, err := m.resolveAppProvider(channel, application...)
	if err != nil {
		return nil, err
	}

	queryable, err := capability[Queryable](provider, channel)
	if err != nil {
		return nil, err
	}

	return queryable.Query(ctx, req)
}

// Close 关闭一笔未支付的收款。若未显式指定 application，则自动使用对应渠道的默认应用。
func (m *Manager) Close(
	ctx context.Context,
	channel Channel,
	req CloseRequest,
	application ...string,
) error {
	provider, err := m.resolveAppProvider(channel, application...)
	if err != nil {
		return err
	}

	payable, err := capability[Payable](provider, channel)
	if err != nil {
		return err
	}

	return payable.Close(ctx, req)
}

// Refund 发起全额或部分退款。若未显式指定 application，则自动使用对应渠道的默认应用。
func (m *Manager) Refund(
	ctx context.Context,
	channel Channel,
	req RefundRequest,
	application ...string,
) (*RefundResult, error) {
	provider, err := m.resolveAppProvider(channel, application...)
	if err != nil {
		return nil, err
	}

	refundable, err := capability[Refundable](provider, channel)
	if err != nil {
		return nil, err
	}

	return refundable.Refund(ctx, req)
}

// QueryRefund 主动查询退款状态。若未显式指定 application，则自动使用对应渠道的默认应用。
func (m *Manager) QueryRefund(
	ctx context.Context,
	channel Channel,
	req QueryRefundRequest,
	application ...string,
) (*RefundResult, error) {
	provider, err := m.resolveAppProvider(channel, application...)
	if err != nil {
		return nil, err
	}

	refundable, err := capability[Refundable](provider, channel)
	if err != nil {
		return nil, err
	}

	return refundable.QueryRefund(ctx, req)
}

// ParseNotification 聚合解析并验签渠道异步通知。
func (m *Manager) ParseNotification(
	_ context.Context,
	channel Channel,
	application string,
	req *http.Request,
) (*Notification, error) {
	provider, err := m.resolveAppProvider(channel, application)
	if err != nil {
		return nil, err
	}

	verifier, err := capability[WebhookVerifiable](provider, channel)
	if err != nil {
		return nil, err
	}

	return verifier.ParseNotification(req)
}

// resolveAppProvider 按渠道和可选应用标识解析 Provider。
func (m *Manager) resolveAppProvider(channel Channel, application ...string) (Provider, error) {
	if !m.isEnabled() {
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
func (m *Manager) For(channel Channel, application string) (Provider, error) {
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
func (m *Manager) ForScene(channel Channel, scene Scene) (Provider, error) {
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
func (m *Manager) Applications(channel Channel) []string {
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
func providerKey(channel Channel, application string) string {
	return channel.String() + "/" + application
}

// sceneOwnerKey 拼接场景归属键。
func sceneOwnerKey(channel Channel, scene Scene) string {
	return channel.String() + "/" + scene.String()
}

// sortedKeys 返回映射的键并按字典序排序，保证启动期构造顺序可复现。
func sortedKeys[V any](values map[string]V) []string {
	return slices.Sorted(maps.Keys(values))
}

// resolveNotifyURL 返回生效的通知地址，请求未覆盖时回退到应用默认配置。
func resolveNotifyURL(override, fallback string) string {
	return cmp.Or(strings.TrimSpace(override), fallback)
}

// resolveCredentialPath 将凭据文件路径与证书根目录合并。
// 绝对路径原样返回；相对路径优先与 baseDir 拼接；若拼接后文件存在则返回拼接路径，否则若相对原路径文件存在则使用原路径。
func resolveCredentialPath(baseDir, path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return trimmed
	}
	base := strings.TrimSpace(baseDir)
	if base == "" {
		return trimmed
	}
	candidate := filepath.Join(base, trimmed)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	if _, err := os.Stat(trimmed); err == nil {
		return trimmed
	}
	return candidate
}

// credentialField 描述待校验的凭据字段与其配置值。
type credentialField struct {
	Name  string
	Value string
}

// requireCredentials 按顺序校验必填凭据，遇到首个缺失项即报错，保证报错顺序确定。
func requireCredentials(channel, appKey string, fields []credentialField) error {
	for _, field := range fields {
		if strings.TrimSpace(field.Value) == "" {
			return errs.ServiceUnavailable(
				fmt.Sprintf("%s application %s is missing required credential: %s", channel, appKey, field.Name),
			)
		}
	}

	return nil
}

// readCredentialFile 读取证书或私钥文件内容。
// 凭据只配置路径，内容在启动期一次性读入内存，文件本身既不入库也不进版本库。
// 若路径为相对路径且传入了 baseDir，将自动以 baseDir 为基准目录补全。
func readCredentialFile(baseDir, path string) ([]byte, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, errs.ServiceUnavailable("payment credential file path is not configured")
	}

	resolved := resolveCredentialPath(baseDir, trimmed)
	content, err := os.ReadFile(resolved)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to read payment credential file %s: %w", resolved, err))
	}
	if len(content) == 0 {
		return nil, errs.ServiceUnavailable("payment credential file is empty: " + resolved)
	}

	return content, nil
}

// decodeRSAPublicKey 从 PEM 内容中解析 RSA 公钥，同时兼容证书与裸公钥两种封装。
// 使用标准库解析而非引入额外依赖，避免为一个十余行的操作扩大直接依赖面。
func decodeRSAPublicKey(content []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(content)
	if block == nil {
		return nil, errors.New("no pem block found in public key content")
	}

	if block.Type == "CERTIFICATE" {
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse certificate: %w", err)
		}

		publicKey, ok := certificate.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("certificate does not carry an rsa public key")
		}

		return publicKey, nil
	}

	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key: %w", err)
	}

	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not an rsa key")
	}

	return publicKey, nil
}

// resolveScenes 解析渠道应用声明的支付场景集合，并校验场景归属于该渠道。
func resolveScenes(raw []string, channel Channel) (map[Scene]struct{}, error) {
	if len(raw) == 0 {
		return nil, errs.BadRequest("payment application declares no scene: " + channel.String())
	}

	scenes := make(map[Scene]struct{}, len(raw))
	for _, item := range raw {
		scene, err := SceneFromString(strings.TrimSpace(item))
		if err != nil {
			return nil, errs.BadRequest(
				fmt.Sprintf("invalid payment scene declared by application for channel %s: %s", channel.String(), item),
			)
		}
		if scene.Channel() != channel {
			return nil, errs.BadRequest(
				fmt.Sprintf("payment scene %s does not belong to channel %s", item, channel.String()),
			)
		}

		scenes[scene] = struct{}{}
	}

	return scenes, nil
}

// sortedScenes 把场景集合展开为按字典序排序的切片，保证对外暴露顺序稳定。
func sortedScenes(scenes map[Scene]struct{}) []Scene {
	return slices.Sorted(maps.Keys(scenes))
}

// validateCreate 校验收款创建请求的公共必填项与场景约束。
func validateCreate(req CreateRequest) error {
	if !req.Scene.IsValid() {
		return errs.BadRequest("invalid payment scene: " + req.Scene.String())
	}
	if strings.TrimSpace(req.OutTradeNo) == "" {
		return errs.BadRequest("out trade no is required")
	}
	if req.EffectiveAmount() <= 0 {
		return errs.BadRequest("payment amount must be positive")
	}
	if strings.TrimSpace(req.Subject) == "" {
		return errs.BadRequest("payment subject is required")
	}
	if req.Scene.RequiresPayerIdentity() && strings.TrimSpace(req.PayerID) == "" {
		return errs.BadRequest("payer identity is required for this payment scene: " + req.Scene.String())
	}

	return nil
}

// validateRefund 校验退款请求的必填项与金额关系。
func validateRefund(req RefundRequest) error {
	if strings.TrimSpace(req.OutTradeNo) == "" {
		return errs.BadRequest("out trade no is required")
	}
	if strings.TrimSpace(req.OutRefundNo) == "" {
		return errs.BadRequest("out refund no is required")
	}
	refundAmount := req.EffectiveRefundAmount()
	if refundAmount <= 0 {
		return errs.BadRequest("refund amount must be positive")
	}
	totalAmount := req.EffectiveTotalAmount()
	if totalAmount > 0 && refundAmount > totalAmount {
		return errs.BadRequest("refund amount must not exceed total amount")
	}

	return nil
}

// FormatYuanAmount 把以分为单位的金额格式化为人民币元字符串（如 12345 -> "123.45"）。
//
// 这是「渠道边界金额编解码器」的编码方向：部分渠道（支付宝）以元为单位传输金额，
// 而本项目内部一律以分为单位存储，跨边界时必须显式换算。
// 不复用 internal/shared/resource 的同名展示格式化函数，避免把 HTTP 响应层引入基础设施层。
func FormatYuanAmount(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}

	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// ParseYuanAmount 把人民币元字符串解析为以分为单位的整型金额，空串视为零。
//
// 这是「渠道边界金额编解码器」的解码方向。全程使用字符串与整数运算，绝不引入浮点误差；
// 小数位超过两位时按分向下取整，避免因渠道回传高精度尾数而产生不存在的分位。
func ParseYuanAmount(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}

	if strings.HasPrefix(trimmed, "-") {
		return 0, fmt.Errorf("negative yuan amount %q is not allowed", value)
	}

	integerPart, fractionPart, _ := strings.Cut(trimmed, ".")
	fractionPart = (fractionPart + "00")[:2]

	yuan, err := strconv.ParseInt(integerPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid yuan amount %q: %w", value, err)
	}

	fen, err := strconv.ParseInt(fractionPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid yuan fraction in %q: %w", value, err)
	}

	return yuan*100 + fen, nil
}

// channelTimeOrNow 解析渠道时间字符串，缺失或无法解析时回退为当前 UTC 时间。
func channelTimeOrNow(value string) time.Time {
	if parsed, ok := parseChannelTime(value); ok {
		return parsed
	}

	return time.Now().UTC()
}

// optionalChannelTime 解析可缺失的渠道时间字符串，缺失或无法解析时返回空指针。
func optionalChannelTime(value string) *time.Time {
	if parsed, ok := parseChannelTime(value); ok {
		return &parsed
	}
	return nil
}

// parseChannelTime 解析渠道可能返回的多种时间格式。
//
// 多布局模糊解析交给 carbon（AGENTS §4.6：非标日期字符串解析统一用 carbon），
// 解析结果统一归一到 UTC 再落库比对。
func parseChannelTime(value string) (time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, false
	}

	parsed := carbon.ParseByLayouts(trimmed, channelTimeLayouts)
	if !parsed.IsValid() {
		return time.Time{}, false
	}

	return parsed.StdTime().UTC(), true
}

// truncateForLog 截断过长的渠道错误原文，防止日志被大报文冲垮。
func truncateForLog(value string) string {
	if len(value) <= maxChannelErrorLength {
		return value
	}

	return value[:maxChannelErrorLength] + "...(truncated)"
}

// upstreamUnavailable 统一包装渠道调用失败：错误码固定为上游不可用，
// 文案携带渠道前缀、应用标识与操作名，便于按应用维度定位问题。
func upstreamUnavailable(channel, appKey, operation string, cause error) error {
	return errs.Wrap(
		errs.CodeServiceUnavailable,
		fmt.Sprintf("%s request failed: application=%s, operation=%s", channel, appKey, operation),
		cause,
	)
}

// xlogAdapter 把 zerolog 适配为 gopay 期望的 xlog.XLogger 接口。
//
// gopay 只通过 SetLogger 暴露日志注入点，接口仅接受可变参数，无法保留结构化字段能力，
// 因此这里统一把参数拼装为单条消息写入消息体。
// gopay 的请求/响应明细日志都在 Debug 级别并可能包含报文片段，生产环境直接丢弃 Debug，
// 不依赖运维把 LOG_LEVEL 调对。
type xlogAdapter struct {
	logger       *zerolog.Logger
	debugEnabled bool
}

// newXLogAdapter 创建 gopay 日志适配器。
// debugEnabled 为假时丢弃全部 Debug 级输出（生产环境直接丢弃报文级日志）。
func newXLogAdapter(l *zerolog.Logger, debugEnabled bool) xlogAdapter {
	sub := cmp.Or(l, logger.Nop()).With().Str("component", "payment_sdk").Logger()
	return xlogAdapter{
		logger:       &sub,
		debugEnabled: debugEnabled,
	}
}

// Debug 记录 Debug 级别日志；生产环境为丢弃。
func (a xlogAdapter) Debug(args ...any) {
	if a.debugEnabled {
		a.logger.Debug().Msg(composeLogMessage(args...))
	}
}

// Info 记录 Info 级别日志。
func (a xlogAdapter) Info(args ...any) { a.logger.Info().Msg(composeLogMessage(args...)) }

// Warn 记录 Warn 级别日志。
func (a xlogAdapter) Warn(args ...any) { a.logger.Warn().Msg(composeLogMessage(args...)) }

// Error 记录 Error 级别日志。
func (a xlogAdapter) Error(args ...any) { a.logger.Error().Msg(composeLogMessage(args...)) }

// Debugf 按格式记录 Debug 级别日志；生产环境为丢弃。
func (a xlogAdapter) Debugf(format string, args ...any) {
	if a.debugEnabled {
		a.logger.Debug().Msgf(format, args...)
	}
}

// Infof 按格式记录 Info 级别日志。
func (a xlogAdapter) Infof(format string, args ...any) { a.logger.Info().Msgf(format, args...) }

// Warnf 按格式记录 Warn 级别日志。
func (a xlogAdapter) Warnf(format string, args ...any) { a.logger.Warn().Msgf(format, args...) }

// Errorf 按格式记录 Error 级别日志。
func (a xlogAdapter) Errorf(format string, args ...any) { a.logger.Error().Msgf(format, args...) }

// composeLogMessage 把可变参数以空格分隔拼装为单条日志消息，空消息回退为占位符。
func composeLogMessage(args ...any) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, fmt.Sprint(arg))
	}
	return cmp.Or(strings.TrimSpace(strings.Join(parts, " ")), paymentLogPlaceholder)
}
