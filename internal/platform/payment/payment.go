// Package payment 提供微信支付与支付宝的多应用渠道抽象、场景路由与通知验签基础设施。
package payment

import (
	"context"
	"errors"
	"net/http"
	"time"

	"bgin/internal/platform/money"
	"bgin/internal/platform/payment/enums"
)

// ErrCapabilityNotSupported 表示渠道或 Provider 未实现请求的操作能力（如不支持退款或查单）。
var ErrCapabilityNotSupported = errors.New("payment: capability not supported by provider")

// Gateway 定义统一支付基础设施门面契约。
//
// 相比浅层的两阶段解析（Resolver ➜ Provider），Gateway 作为深模块封装了：
//   - 场景（Scene）到具体渠道应用（Channel Application）的自动路由派发；
//   - 单应用场景下的默认应用兜底；
//   - 异步通知（Webhook）的一站式解析与验签；
//   - 启动期多应用健康检查与可用性状态探测。
type Gateway interface {
	// Enabled 返回支付模块是否启用了任一渠道。
	Enabled() bool
	// Create 发起一次收款，内部自动根据 req.Scene 路由到声明了该场景的渠道应用。
	Create(ctx context.Context, req CreateRequest) (*Instruction, error)
	// Query 主动查询支付状态。若未显式指定 application，则自动使用对应渠道的默认应用。
	Query(ctx context.Context, channel enums.PaymentChannel, req QueryRequest, application ...string) (*PaymentResult, error)
	// Close 关闭一笔未支付的收款。若未显式指定 application，则自动使用对应渠道的默认应用。
	Close(ctx context.Context, channel enums.PaymentChannel, req CloseRequest, application ...string) error
	// Refund 发起全额或部分退款。若未显式指定 application，则自动使用对应渠道的默认应用。
	Refund(ctx context.Context, channel enums.PaymentChannel, req RefundRequest, application ...string) (*RefundResult, error)
	// QueryRefund 主动查询退款状态。若未显式指定 application，则自动使用对应渠道的默认应用。
	QueryRefund(ctx context.Context, channel enums.PaymentChannel, req QueryRefundRequest, application ...string) (*RefundResult, error)
	// ParseNotification 聚合解析并验签渠道异步通知。
	ParseNotification(ctx context.Context, channel enums.PaymentChannel, application string, req *http.Request) (*Notification, error)
	// For 按「渠道 + 应用标识」显式解析 Provider（保留高级自定义退路）。
	For(channel enums.PaymentChannel, application string) (Provider, error)
	// ForScene 按「渠道 + 支付场景」显式解析 Provider。
	ForScene(channel enums.PaymentChannel, scene enums.PaymentScene) (Provider, error)
	// Applications 返回指定渠道下已注册的应用标识。
	Applications(channel enums.PaymentChannel) []string
	// HealthCheck 检查全部渠道应用的健康状态。
	HealthCheck() error
}

// Payable 定义渠道收款创建与关单能力契约。
type Payable interface {
	// Create 发起一次收款，返回客户端把付款人交给渠道所需的支付指令。
	Create(ctx context.Context, req CreateRequest) (*Instruction, error)
	// Close 关闭一笔未支付的收款。
	Close(ctx context.Context, req CloseRequest) error
}

// Queryable 定义渠道支付状态主动查询能力契约。
type Queryable interface {
	// Query 主动查询支付状态，用于补偿缺失的渠道通知。
	Query(ctx context.Context, req QueryRequest) (*PaymentResult, error)
}

// Refundable 定义渠道退款与退款查询能力契约。
type Refundable interface {
	// Refund 发起全额或部分退款。
	Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)
	// QueryRefund 主动查询退款状态。
	QueryRefund(ctx context.Context, req QueryRefundRequest) (*RefundResult, error)
}

// WebhookVerifiable 定义异步通知解密与验签能力契约。
type WebhookVerifiable interface {
	// ParseNotification 解析并验签渠道异步通知，返回值中的报文为渠道解密后的业务负载。
	ParseNotification(req *http.Request) (*Notification, error)
}

// HealthChecker 定义渠道应用的凭据与连通性可用状态探测契约。
type HealthChecker interface {
	// HealthCheck 检查该渠道应用的凭据与客户端是否处于可用状态。
	HealthCheck() error
}

// Provider 定义单一渠道应用的基础规范化契约 (遵循 ISP 原则解耦各原子能力)。
type Provider interface {
	// Channel 返回该 Provider 所属的支付渠道。
	Channel() enums.PaymentChannel
	// Application 返回该 Provider 绑定的渠道应用标识。
	Application() string
	// Scenes 返回该渠道应用声明可承载的支付场景。
	Scenes() []enums.PaymentScene

	HealthChecker
}

// FullProvider 组合了支付全链路能力的完整提供者接口。
type FullProvider interface {
	Provider
	Payable
	Queryable
	Refundable
	WebhookVerifiable
}

// CreateRequest 描述一次收款创建请求，字段为渠道中立的规范化语义。
type CreateRequest struct {
	// Scene 支付场景，决定调用渠道的哪个下单接口以及需要哪些附加参数。
	Scene enums.PaymentScene
	// OutTradeNo 我方商户订单号，在同一渠道内唯一且重试时保持不变。
	OutTradeNo string
	// Amount 收款金额，单位为分（当 Money 为 nil 时作为备选生效，保持向后兼容）。
	Amount int64
	// Money 高精度金融货币值对象。优先采用 Money，并通过 Money.ToCents() 自动遵循 ISO-4217 币种精度与金融舍入。
	Money *money.Money
	// Subject 收款主题，展示在渠道账单中。
	Subject string
	// Description 收款描述。支付宝用作账单明细（body），微信忽略该字段。
	Description string
	// ExpireAt 该笔收款的过期时刻，渠道侧同步按此时间失效。
	ExpireAt time.Time
	// NotifyURL 渠道通知地址，留空时使用渠道应用的默认配置。
	NotifyURL string
	// ReturnURL 同步回跳地址，仅支付宝电脑网站与手机网站支付需要。
	ReturnURL string
	// PayerID 付款人的渠道身份，微信为 openid、支付宝小程序为 buyer_id。
	// 仅 enums.PaymentScene.RequiresPayerIdentity 为真的场景必填。
	PayerID string
	// ClientIP 付款人客户端 IP，微信 H5 支付必填。
	ClientIP string
	// Attach 渠道原样回传的附加数据。
	Attach string
}

// EffectiveAmount 返回实际生效的收款金额（单位：分）。
// 优先使用 Money 值对象；若 Money 为 nil 则回退到 Amount 字段。
func (r CreateRequest) EffectiveAmount() int64 {
	if r.Money != nil {
		return r.Money.ToCents()
	}
	return r.Amount
}

// EffectiveMoney 返回实际生效的 Money 值对象。
// 若 Money 字段非空则直接返回解引用副本；否则基于 EffectiveAmount 与 CNY 币种构造默认 Money。
func (r CreateRequest) EffectiveMoney() money.Money {
	if r.Money != nil {
		return *r.Money
	}
	m, _ := money.NewFromCents(r.Amount, "CNY")
	return m
}

// Instruction 描述客户端如何把付款人交给渠道完成支付。
// Action 与 Value/Params 的组合由渠道返回物归一而来：
//   - qr_code：Value 为二维码链接，由客户端自行渲染；
//   - redirect_url：Value 为收银台跳转地址；
//   - invoke_params：Params 为渠道二次签名后的参数集合。
type Instruction struct {
	// Scene 该指令对应的支付场景。
	Scene enums.PaymentScene
	// Action 客户端应当执行的动作。
	Action enums.PaymentInstructionAction
	// Value 二维码链接或跳转地址，动作非 invoke_params 时有效。
	Value string
	// Params 调起渠道 SDK 所需的签名参数集合，动作为 invoke_params 时有效。
	Params map[string]string
}

// QueryRequest 描述一次支付状态主动查询。
type QueryRequest struct {
	// OutTradeNo 我方商户订单号。
	OutTradeNo string
}

// CloseRequest 描述一次关闭未支付收款。
type CloseRequest struct {
	// OutTradeNo 我方商户订单号。
	OutTradeNo string
}

// RefundRequest 描述一次退款请求。
type RefundRequest struct {
	// OutTradeNo 原收款的商户订单号。
	OutTradeNo string
	// OutRefundNo 商户退款单号，重试时必须保持不变以保证幂等。
	OutRefundNo string
	// TotalAmount 原收款总额，单位为分（当 TotalMoney 为 nil 时生效）。
	TotalAmount int64
	// RefundAmount 本次退款金额，单位为分，可小于总额以实现部分退款（当 RefundMoney 为 nil 时生效）。
	RefundAmount int64
	// TotalMoney 原收款总额高精度货币值对象（可选）。
	TotalMoney *money.Money
	// RefundMoney 本次退款金额高精度货币值对象（可选）。
	RefundMoney *money.Money
	// Reason 退款原因，展示在渠道账单中。
	Reason string
	// NotifyURL 退款结果通知地址，留空时使用渠道应用的默认配置。
	NotifyURL string
}

// EffectiveTotalAmount 返回实际生效的原收款总额（单位：分）。
func (r RefundRequest) EffectiveTotalAmount() int64 {
	if r.TotalMoney != nil {
		return r.TotalMoney.ToCents()
	}
	return r.TotalAmount
}

// EffectiveRefundAmount 返回实际生效的本次退款金额（单位：分）。
func (r RefundRequest) EffectiveRefundAmount() int64 {
	if r.RefundMoney != nil {
		return r.RefundMoney.ToCents()
	}
	return r.RefundAmount
}

// QueryRefundRequest 描述一次退款状态主动查询。
type QueryRefundRequest struct {
	// OutTradeNo 原收款的商户订单号。
	OutTradeNo string
	// OutRefundNo 商户退款单号。
	OutRefundNo string
}

// PaymentResult 是渠道支付状态的规范化结果。
type PaymentResult struct {
	// OutTradeNo 我方商户订单号。
	OutTradeNo string
	// ChannelTradeNo 渠道交易号。
	ChannelTradeNo string
	// Status 归一化后的支付状态。
	Status enums.PaymentStatus
	// ChannelState 渠道返回的原始状态字符串，供审计与排障使用。
	ChannelState string
	// PaidAmount 实际到账金额，单位为分。
	PaidAmount int64
	// Money 实际到账金额对应的高精度货币值对象（可选）。
	Money *money.Money
	// PaidAt 支付完成时刻，未支付时为空。
	PaidAt *time.Time
	// PayerID 付款人的渠道身份。
	PayerID string
}

// WithMoney 辅助链式注入高精度货币值对象。
func (p *PaymentResult) WithMoney(m money.Money) *PaymentResult {
	if p != nil {
		p.Money = &m
	}
	return p
}

// RefundResult 是渠道退款状态的规范化结果。
type RefundResult struct {
	// OutRefundNo 我方商户退款单号。
	OutRefundNo string
	// OutTradeNo 本次退款对应的原收款商户订单号，用于定位退款所属的支付。
	OutTradeNo string
	// ChannelRefundNo 渠道退款单号。
	// 支付宝不在接口层面暴露退款流水号，该字段在支付宝渠道恒为空；
	// 需要关联原收款时一律使用 OutTradeNo，不得以支付交易号冒充退款单号。
	ChannelRefundNo string
	// Status 归一化后的退款状态。
	Status enums.PaymentRefundStatus
	// ChannelState 渠道返回的原始状态字符串，供审计与排障使用。
	ChannelState string
	// RefundAmount 本次退款金额，单位为分。
	RefundAmount int64
	// Money 本次退款金额对应的高精度货币值对象（可选）。
	Money *money.Money
	// RefundedAt 退款成功时刻，未成功时为空。
	RefundedAt *time.Time
}

// WithMoney 辅助链式注入高精度货币值对象。
func (r *RefundResult) WithMoney(m money.Money) *RefundResult {
	if r != nil {
		r.Money = &m
	}
	return r
}

// NotificationAck 描述渠道要求的通知应答内容。
//
// 渠道以「是否收到规定应答」判定通知投递成功，因此应答报文由渠道协议决定，
// 并显式豁免本项目统一的 JSON 响应信封：
//   - 微信要求 5 秒内应答 JSON 成功报文，超时会重试；
//   - 支付宝要求裸文本 success，否则按约 4m/10m/10m/1h/2h/6h/15h 的间隔重试，最长 24 小时 22 分钟。
type NotificationAck struct {
	// ContentType 应答的 Content-Type。
	ContentType string
	// Body 应答报文原文。
	Body string
}

// 渠道通知应答报文。
//
// 渠道只认自己规定的成功报文，其余内容一律判定为投递失败并按重试策略重发，
// 因此成功与失败两种应答都必须由渠道协议决定，属于渠道级常量而非应用级行为。
const (
	wechatAckContentType = "application/json; charset=utf-8"
	wechatSuccessAckBody = `{"code":"SUCCESS","message":"OK"}`
	wechatFailureAckBody = `{"code":"FAIL","message":"FAIL"}`

	alipayAckContentType = "text/plain; charset=utf-8"
	alipaySuccessAckBody = "success"
	alipayFailureAckBody = "fail"

	plainTextContentType  = "text/plain; charset=utf-8"
	unknownChannelAckBody = "fail"
)

// notificationAcks 汇总各渠道的成功与失败应答报文。
var notificationAcks = map[enums.PaymentChannel]struct {
	Success NotificationAck
	Failure NotificationAck
}{
	enums.PaymentChannelWechat: {
		Success: NotificationAck{ContentType: wechatAckContentType, Body: wechatSuccessAckBody},
		Failure: NotificationAck{ContentType: wechatAckContentType, Body: wechatFailureAckBody},
	},
	enums.PaymentChannelAlipay: {
		Success: NotificationAck{ContentType: alipayAckContentType, Body: alipaySuccessAckBody},
		Failure: NotificationAck{ContentType: alipayAckContentType, Body: alipayFailureAckBody},
	},
}

// SuccessAck 返回指定渠道要求的通知成功应答。
func SuccessAck(channel enums.PaymentChannel) NotificationAck {
	if ack, ok := notificationAcks[channel]; ok {
		return ack.Success
	}

	return NotificationAck{ContentType: plainTextContentType, Body: unknownChannelAckBody}
}

// FailureAck 返回指定渠道要求的通知失败应答。
//
// 失败应答同样必须符合渠道协议：渠道据此判定本次投递失败并按重试策略重发。
// 未收录的渠道返回纯文本失败应答兜底——任何不等于渠道成功报文的内容都会被判定为失败，
// 因此兜底不会导致通知被静默吞掉。
func FailureAck(channel enums.PaymentChannel) NotificationAck {
	if ack, ok := notificationAcks[channel]; ok {
		return ack.Failure
	}

	return NotificationAck{ContentType: plainTextContentType, Body: unknownChannelAckBody}
}

// Notification 是渠道异步通知的规范化结果。
type Notification struct {
	// Kind 通知类型，支付通知与退款通知共用同一回调端点。
	Kind enums.PaymentNotificationKind
	// ID 渠道通知 ID，可作为回调报文的幂等键。
	ID string
	// Event 渠道原始事件类型或交易状态。
	Event string
	// OccurredAt 通知产生时刻。
	OccurredAt time.Time
	// Payload 渠道解密后的业务负载，用于落库审计与排障。
	Payload []byte
	// Payment 支付通知的规范化结果，Kind 为 payment 时非空。
	Payment *PaymentResult
	// Refund 退款通知的规范化结果，Kind 为 refund 时非空。
	Refund *RefundResult
}
