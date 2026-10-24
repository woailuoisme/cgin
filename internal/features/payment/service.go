package payment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/samber/oops"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/eventbus"
	"bgin/internal/platform/logger"
	platformPayment "bgin/internal/platform/payment"
	"bgin/internal/platform/payment/enums"
	"bgin/internal/shared/events"
)

const (
	// notifyLockTTL 是防止渠道并发通知重放的分布式锁持有时间。
	notifyLockTTL = 30 * time.Second
	// notifyDoneTTL 是已消费通知的持久去重标记保留时长。
	notifyDoneTTL = 7 * 24 * time.Hour
	// errUnsupportedChannel 是不支持支付渠道时的统一错误信息。
	errUnsupportedChannel = "Unsupported payment channel"
)

// CallbackHandler 是支付与退款业务消费钩子接口。
// 业务切片或聚合根通过实现此接口接收成功状态并推进领域状态机。
//
// 实现方必须自行完成订单归属与金额核对：result.PaidAmount / result.RefundAmount
// 来自渠道报文，未与本系统订单金额比对前不得直接置为已支付/已退款。
// 验签只能证明报文来自渠道，不能证明金额与本地订单一致。
type CallbackHandler interface {
	// OnPaymentSuccess 处理支付成功回调通知。返回 nil 代表业务成功消费，由网关向渠道返回成功 ACK。
	OnPaymentSuccess(ctx context.Context, result *platformPayment.PaymentResult) error
	// OnRefundSuccess 处理退款成功或状态变更通知。返回 nil 代表业务成功消费。
	OnRefundSuccess(ctx context.Context, result *platformPayment.RefundResult) error
}

// defaultCallbackHandler 是未接入业务处理器时的兜底实现。
//
// 它刻意不回执成功：静默 ACK 会让渠道停止重试，订单永远停留在未支付状态且无人察觉；
// 返回失败会触发渠道重试，让"支付模块未接线"这件事在监控上暴露出来。
type defaultCallbackHandler struct{}

var _ CallbackHandler = (*defaultCallbackHandler)(nil)

func (d *defaultCallbackHandler) OnPaymentSuccess(ctx context.Context, result *platformPayment.PaymentResult) error {
	logger.FromContext(ctx).Error("payment success callback received without a registered handler",
		zap.String("out_trade_no", result.OutTradeNo),
		zap.String("channel_trade_no", result.ChannelTradeNo),
		zap.Int64("paid_amount", result.PaidAmount),
	)
	return errs.Internal(errors.New("payment callback handler is not configured"))
}

func (d *defaultCallbackHandler) OnRefundSuccess(ctx context.Context, result *platformPayment.RefundResult) error {
	logger.FromContext(ctx).Error("refund callback received without a registered handler",
		zap.String("out_trade_no", result.OutTradeNo),
		zap.String("out_refund_no", result.OutRefundNo),
		zap.String("channel_refund_no", result.ChannelRefundNo),
		zap.Int64("refund_amount", result.RefundAmount),
		zap.String("status", string(result.Status)),
	)
	return errs.Internal(errors.New("payment callback handler is not configured"))
}

// Service 负责协调支付回调的分布式幂等防重与业务事件分发。
type Service struct {
	gateway   platformPayment.Gateway
	cache     cache.Store
	cfg       *config.Config
	publisher eventbus.Publisher
	handler   CallbackHandler
}

// NewService 构造支付回调协调服务。publisher 允许为 nil，表示不向外广播领域事件。
func NewService(
	gateway platformPayment.Gateway,
	cacheClient cache.Store,
	cfg *config.Config,
	publisher eventbus.Publisher,
) *Service {
	return &Service{
		gateway:   gateway,
		cache:     cacheClient,
		cfg:       cfg,
		publisher: publisher,
		handler:   &defaultCallbackHandler{},
	}
}

// Gateway 暴露底层支付网关接口。
func (s *Service) Gateway() platformPayment.Gateway {
	if s == nil {
		return nil
	}
	return s.gateway
}

// SetCallbackHandler 注册业务消费钩子实现。
func (s *Service) SetCallbackHandler(handler CallbackHandler) {
	if handler != nil {
		s.handler = handler
	}
}

// ProcessNotification 统一处理已通过传输层验签解析的支付/退款异步通知。
func (s *Service) ProcessNotification(
	ctx context.Context,
	channel enums.PaymentChannel,
	notification *platformPayment.Notification,
) (platformPayment.NotificationAck, error) {
	log := logger.FromContext(ctx)

	if !channel.IsValid() {
		return platformPayment.FailureAck(channel), errs.BadRequest(errUnsupportedChannel)
	}
	if notification == nil {
		return platformPayment.FailureAck(channel), errs.BadRequest("notification payload is nil")
	}

	key := notificationKey{
		channel: channel,
		kind:    notification.Kind.String(),
		tradeNo: s.extractTradeNo(notification),
	}

	// 1. 幂等与并发防重放控制（按"渠道 + 通知类型 + 单号"三段去重：
	//    同一订单的支付与退款通知必须各算一次，否则退款会被支付的去重标记吞掉）
	consumed, lockErr := s.acquireLockOrConsumed(ctx, key)
	if lockErr != nil {
		return platformPayment.FailureAck(channel), lockErr
	}
	if consumed {
		return platformPayment.SuccessAck(channel), nil
	}

	// 2. 分发到具体业务处理器与发布领域事件
	handleErr := s.dispatchNotification(ctx, channel, notification)
	if handleErr != nil {
		return platformPayment.FailureAck(channel), oops.
			Code(string(errs.CodeInternal)).
			With("channel", key.channel.String()).
			With("kind", key.kind).
			With("trade_no", key.tradeNo).
			Wrap(handleErr)
	}

	log.Info("payment callback notification successfully processed", key.logFields()...)

	// 3. 写入持久化去重标记
	s.recordConsumed(ctx, key)

	return platformPayment.SuccessAck(channel), nil
}

// notificationKey 是支付通知的三段幂等标识：渠道 + 通知类型 + 单号。
//
// 三者总是一起出行（缓存键、日志、错误上下文），因此收进一个类型，
// 避免每个下游 helper 都摊开三个参数、且任何一处漏传一段就退化成串号。
type notificationKey struct {
	channel enums.PaymentChannel
	kind    string
	tradeNo string
}

// notificationKeyPurpose 是幂等缓存键的用途维度：已消费标记与处理中锁必须分键，否则会互相覆盖。
type notificationKeyPurpose string

const (
	purposeConsumed notificationKeyPurpose = "done"
	purposeLock     notificationKeyPurpose = "lock"
)

// cacheKey 返回指定用途的幂等缓存键，三段标识的拼装只此一处。
func (k notificationKey) cacheKey(purpose notificationKeyPurpose) string {
	return cache.Key("payment", "notify", string(purpose), k.channel.String(), k.kind, k.tradeNo)
}

// logFields 返回统一的日志字段，保证幂等键在日志里始终三段齐全。
func (k notificationKey) logFields() []zap.Field {
	return []zap.Field{
		zap.String("channel", k.channel.String()),
		zap.String("kind", k.kind),
		zap.String("trade_no", k.tradeNo),
	}
}

// acquireLockOrConsumed 检查通知是否已消费并尝试加分布式防重放锁。
func (s *Service) acquireLockOrConsumed(ctx context.Context, key notificationKey) (bool, error) {
	if s.cache == nil || key.tradeNo == "" {
		return false, nil
	}
	log := logger.FromContext(ctx)

	doneKey := key.cacheKey(purposeConsumed)
	if _, getErr := s.cache.Get(ctx, doneKey); getErr == nil {
		log.Warn("callback notification already consumed, acknowledged without reprocessing",
			key.logFields()...,
		)
		return true, nil
	}

	lockKey := key.cacheKey(purposeLock)
	acquired, lockErr := s.cache.SetNX(ctx, lockKey, "1", notifyLockTTL)
	if lockErr == nil && !acquired {
		return false, errs.Conflict("duplicate notification in progress")
	}

	return false, nil
}

// dispatchNotification 将通知派发给业务处理器并向事件总线广播领域事件。
func (s *Service) dispatchNotification(ctx context.Context, channel enums.PaymentChannel, n *platformPayment.Notification) error {
	switch n.Kind {
	case enums.PaymentNotificationKindPayment:
		if n.Payment == nil {
			return errs.BadRequest("payment notification payload is missing")
		}
		if err := s.handler.OnPaymentSuccess(ctx, n.Payment); err != nil {
			return err
		}
		if s.publisher != nil {
			_ = s.publisher.Publish(ctx, events.PaymentSucceededEvent{
				Channel:   channel,
				Payment:   n.Payment,
				Timestamp: time.Now(),
			})
		}
		return nil

	case enums.PaymentNotificationKindRefund:
		if n.Refund == nil {
			return errs.BadRequest("refund notification payload is missing")
		}
		if err := s.handler.OnRefundSuccess(ctx, n.Refund); err != nil {
			return err
		}
		if s.publisher != nil {
			_ = s.publisher.Publish(ctx, events.PaymentRefundedEvent{
				Channel:   channel,
				Refund:    n.Refund,
				Timestamp: time.Now(),
			})
		}
		return nil

	default:
		return errs.BadRequest("unsupported payment notification kind: " + n.Kind.String())
	}
}

// recordConsumed 写入持久化去重标记。
func (s *Service) recordConsumed(ctx context.Context, key notificationKey) {
	if s.cache == nil || key.tradeNo == "" {
		return
	}
	doneKey := key.cacheKey(purposeConsumed)
	if err := s.cache.Set(ctx, doneKey, "1", notifyDoneTTL); err != nil {
		logger.FromContext(ctx).Warn("failed to persist payment notification dedup marker",
			zap.Error(err),
			zap.String("trade_no", key.tradeNo),
		)
	}
}

func (s *Service) baseURL() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	return strings.TrimRight(s.cfg.App.URL, "/")
}

// FailureRedirectURL 返回支付失败后引导用户回到的订单结果页地址。
func (s *Service) FailureRedirectURL() string {
	return s.baseURL() + "/orders?status=failed"
}

// BuildAlipayReturnURL 构造支付宝同步跳转成功后的前端页面完整地址。
func (s *Service) BuildAlipayReturnURL(outTradeNo string) string {
	return fmt.Sprintf("%s/orders/%s?status=success", s.baseURL(), outTradeNo)
}

// extractTradeNo 提取用于并发排重的交易标识号。
func (s *Service) extractTradeNo(n *platformPayment.Notification) string {
	if n == nil {
		return ""
	}
	if n.Payment != nil && n.Payment.OutTradeNo != "" {
		return n.Payment.OutTradeNo
	}
	if n.Refund != nil && n.Refund.OutRefundNo != "" {
		return n.Refund.OutRefundNo
	}

	return n.ID
}
