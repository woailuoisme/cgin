package payment

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/samber/lo"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/payment/enums"
)

// Fake 注入操作名常量，用于按操作注入故障。
const (
	FakeOperationCreate            = "create"
	FakeOperationQuery             = "query"
	FakeOperationClose             = "close"
	FakeOperationRefund            = "refund"
	FakeOperationQueryRefund       = "query_refund"
	FakeOperationParseNotification = "parse_notification"
	FakeOperationHealthCheck       = "health_check"
)

// FakeProvider 是进程内的支付 Provider 实现，用于单元测试与本地开发。
//
// 行为约定：
//   - 不发起任何网络请求，也不做真实签名与验签，渠道行为全部由调用方显式注入；
//   - 未注册的支付或退款查询会返回确定的 pending 结果，便于测试在最小设置下推进；
//   - 通知必须由 QueueNotification 预先入队，否则解析一律报错，避免测试隐式依赖真实报文。
type FakeProvider struct {
	mu sync.RWMutex

	channel     enums.PaymentChannel
	application string

	instruction   *Instruction
	payments      map[string]*PaymentResult
	refunds       map[string]*RefundResult
	queuedNotices []*Notification

	createdRequests  []CreateRequest
	closedRequests   []CloseRequest
	queryRequests    []QueryRequest
	refundRequests   []RefundRequest
	refundQueries    []QueryRefundRequest
	notificationReqs []*http.Request
	failures         map[string]error
}

var _ FullProvider = (*FakeProvider)(nil)

// NewFakeProvider 创建绑定到指定渠道与应用的 Fake Provider。
func NewFakeProvider(channel enums.PaymentChannel, application string) *FakeProvider {
	return &FakeProvider{
		channel:     channel,
		application: application,
		payments:    make(map[string]*PaymentResult),
		refunds:     make(map[string]*RefundResult),
		failures:    make(map[string]error),
	}
}

// Channel 返回 Fake 绑定的支付渠道。
func (f *FakeProvider) Channel() enums.PaymentChannel { return f.channel }

// Application 返回 Fake 绑定的应用标识。
func (f *FakeProvider) Application() string { return f.application }

// Scenes 声明 Fake 支持指定渠道下的全部合法场景。
func (f *FakeProvider) Scenes() []enums.PaymentScene {
	return lo.Filter(enums.PaymentSceneValues(), func(scene enums.PaymentScene, _ int) bool {
		return scene.Channel() == f.channel
	})
}

// SetInstruction 注入下单返回值；未注入时 Create 会返回确定性的默认二维码指令。
func (f *FakeProvider) SetInstruction(instruction *Instruction) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instruction = instruction
}

// SetPaymentResult 为指定商户订单号注入支付查询结果。
func (f *FakeProvider) SetPaymentResult(outTradeNo string, result *PaymentResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payments[outTradeNo] = result
}

// SetRefundResult 为指定商户退款单号注入退款查询结果。
func (f *FakeProvider) SetRefundResult(outRefundNo string, result *RefundResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refunds[outRefundNo] = result
}

// MarkPaid 把指定商户订单号标记为支付成功。
func (f *FakeProvider) MarkPaid(outTradeNo string, amount int64) {
	paidAt := time.Now().UTC()
	f.SetPaymentResult(outTradeNo, &PaymentResult{
		OutTradeNo:     outTradeNo,
		ChannelTradeNo: "fake-trade-" + outTradeNo,
		Status:         enums.PaymentStatusSucceeded,
		ChannelState:   string(enums.PaymentStatusSucceeded),
		PaidAmount:     amount,
		PaidAt:         &paidAt,
	})
}

// MarkRefundSucceeded 把指定商户退款单号标记为退款成功。
func (f *FakeProvider) MarkRefundSucceeded(outRefundNo string, amount int64) {
	refundedAt := time.Now().UTC()
	f.SetRefundResult(outRefundNo, &RefundResult{
		OutRefundNo:     outRefundNo,
		ChannelRefundNo: "fake-refund-" + outRefundNo,
		Status:          enums.PaymentRefundStatusSucceeded,
		ChannelState:    string(enums.PaymentRefundStatusSucceeded),
		RefundAmount:    amount,
		RefundedAt:      &refundedAt,
	})
}

// QueueNotification 入队一条待解析的渠道通知。
func (f *FakeProvider) QueueNotification(notification *Notification) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queuedNotices = append(f.queuedNotices, notification)
}

// FailOperation 为指定操作注入故障，传 nil 可清除。
func (f *FakeProvider) FailOperation(operation string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err == nil {
		delete(f.failures, operation)
		return
	}

	f.failures[operation] = err
}

// cloneNonNil 复制切片，并保证 nil 输入返回非 nil 空切片，维持 FakeProvider 既有的对外语义。
func cloneNonNil[T any](in []T) []T {
	out := make([]T, len(in))
	copy(out, in)

	return out
}

// Created 返回全部下单请求的副本。
func (f *FakeProvider) Created() []CreateRequest {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return cloneNonNil(f.createdRequests)
}

// LastCreated 返回最后一次下单请求。
func (f *FakeProvider) LastCreated() (CreateRequest, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return lo.Last(f.createdRequests)
}

// Closed 返回全部关单请求的副本。
func (f *FakeProvider) Closed() []CloseRequest {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return cloneNonNil(f.closedRequests)
}

// Queried 返回全部支付查询请求的副本。
func (f *FakeProvider) Queried() []QueryRequest {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return cloneNonNil(f.queryRequests)
}

// Refunded 返回全部退款请求的副本。
func (f *FakeProvider) Refunded() []RefundRequest {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return cloneNonNil(f.refundRequests)
}

// RefundQueried 返回全部退款查询请求的副本。
func (f *FakeProvider) RefundQueried() []QueryRefundRequest {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return cloneNonNil(f.refundQueries)
}

// NotificationRequests 返回交到 ParseNotification 的全部 HTTP 请求，用于断言传输层构造的请求。
func (f *FakeProvider) NotificationRequests() []*http.Request {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return cloneNonNil(f.notificationReqs)
}

// Reset 清空全部注入状态与调用记录。
func (f *FakeProvider) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.instruction = nil
	f.payments = make(map[string]*PaymentResult)
	f.refunds = make(map[string]*RefundResult)
	f.queuedNotices = nil
	f.createdRequests = nil
	f.closedRequests = nil
	f.queryRequests = nil
	f.refundRequests = nil
	f.refundQueries = nil
	f.notificationReqs = nil
	f.failures = make(map[string]error)
}

// HealthCheck 返回注入的健康检查故障。
func (f *FakeProvider) HealthCheck() error {
	return f.injectedError(FakeOperationHealthCheck)
}

// Create 返回注入的下单指令，未注入时返回确定性的默认二维码指令。
func (f *FakeProvider) Create(_ context.Context, req CreateRequest) (*Instruction, error) {
	if err := f.injectedError(FakeOperationCreate); err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.createdRequests = append(f.createdRequests, req)
	instruction := f.instruction
	f.mu.Unlock()

	if instruction != nil {
		return instruction, nil
	}

	return &Instruction{
		Scene:  req.Scene,
		Action: enums.PaymentInstructionQRCode,
		Value:  "fake://pay/" + req.OutTradeNo,
	}, nil
}

// Query 返回注入的支付结果，未注入时返回 pending。
func (f *FakeProvider) Query(_ context.Context, req QueryRequest) (*PaymentResult, error) {
	if err := f.injectedError(FakeOperationQuery); err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.queryRequests = append(f.queryRequests, req)
	result, ok := f.payments[req.OutTradeNo]
	f.mu.Unlock()

	if ok {
		return result, nil
	}

	return &PaymentResult{
		OutTradeNo:   req.OutTradeNo,
		Status:       enums.PaymentStatusPending,
		ChannelState: string(enums.PaymentStatusPending),
	}, nil
}

// Close 记录关单请求。
func (f *FakeProvider) Close(_ context.Context, req CloseRequest) error {
	if err := f.injectedError(FakeOperationClose); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedRequests = append(f.closedRequests, req)

	return nil
}

// Refund 返回注入的退款结果，未注入时返回 pending。
func (f *FakeProvider) Refund(_ context.Context, req RefundRequest) (*RefundResult, error) {
	if err := f.injectedError(FakeOperationRefund); err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.refundRequests = append(f.refundRequests, req)
	result, ok := f.refunds[req.OutRefundNo]
	f.mu.Unlock()

	if ok {
		return result, nil
	}

	return &RefundResult{
		OutRefundNo:  req.OutRefundNo,
		OutTradeNo:   req.OutTradeNo,
		Status:       enums.PaymentRefundStatusPending,
		ChannelState: string(enums.PaymentRefundStatusPending),
		RefundAmount: req.EffectiveRefundAmount(),
	}, nil
}

// QueryRefund 返回注入的退款查询结果，未注入时返回 pending。
func (f *FakeProvider) QueryRefund(_ context.Context, req QueryRefundRequest) (*RefundResult, error) {
	if err := f.injectedError(FakeOperationQueryRefund); err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.refundQueries = append(f.refundQueries, req)
	result, ok := f.refunds[req.OutRefundNo]
	f.mu.Unlock()

	if ok {
		return result, nil
	}

	return &RefundResult{
		OutRefundNo:  req.OutRefundNo,
		Status:       enums.PaymentRefundStatusPending,
		ChannelState: string(enums.PaymentRefundStatusPending),
	}, nil
}

// ParseNotification 返回预先入队的通知，队列为空时报错。
func (f *FakeProvider) ParseNotification(req *http.Request) (*Notification, error) {
	if err := f.injectedError(FakeOperationParseNotification); err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.notificationReqs = append(f.notificationReqs, req)

	if len(f.queuedNotices) == 0 {
		return nil, errs.BadRequest("no fake notification queued: " + f.application)
	}

	notification := f.queuedNotices[0]
	f.queuedNotices = f.queuedNotices[1:]

	return notification, nil
}

// injectedError 返回指定操作注入的故障。
func (f *FakeProvider) injectedError(operation string) error {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return f.failures[operation]
}
