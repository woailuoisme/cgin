// Package payment 提供微信支付与支付宝的多应用渠道抽象、场景路由与通知验签基础设施。
package payment

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"cgin/pkg/money"
)

// ErrCapabilityNotSupported 表示渠道或 Provider 未实现请求的操作能力（如不支持退款或查单）。
var ErrCapabilityNotSupported = errors.New("payment: capability not supported by provider")

// Channel 代表支付渠道领域枚举。
// 与认证域的 ProviderID 刻意区分：后者描述身份绑定，本枚举描述资金通道。
type Channel string

// 支付渠道常量定义。
const (
	// ChannelWechat 微信支付渠道。
	ChannelWechat Channel = "wechat"
	// ChannelAlipay 支付宝渠道。
	ChannelAlipay Channel = "alipay"
)

// String 返回支付渠道的字符串表现形式。
func (c Channel) String() string { return string(c) }

// IsValid 检查支付渠道是否合法。
func (c Channel) IsValid() bool {
	return c == ChannelWechat || c == ChannelAlipay
}

// ChannelValues 获取全部支付渠道枚举值。
func ChannelValues() []Channel {
	return []Channel{ChannelWechat, ChannelAlipay}
}

// Scene 代表支付场景领域枚举，即渠道内的具体交互方式。
// 场景决定使用哪个渠道应用，以及付款人必须提供哪些参数，因此属于支付请求的一部分。
type Scene string

// 支付场景常量定义。取值以「渠道_方式」前缀区分，便于日志检索与排障。
const (
	// SceneWechatNative 微信 Native 扫码支付。
	SceneWechatNative Scene = "wechat_native"
	// SceneWechatJsapi 微信公众号 JSAPI 支付。
	SceneWechatJsapi Scene = "wechat_jsapi"
	// SceneWechatMiniProgram 微信小程序支付。
	SceneWechatMiniProgram Scene = "wechat_miniprogram"
	// SceneWechatH5 微信 H5 支付。
	SceneWechatH5 Scene = "wechat_h5"
	// SceneWechatApp 微信 APP 支付。
	SceneWechatApp Scene = "wechat_app"
	// SceneAlipayPage 支付宝电脑网站支付。
	SceneAlipayPage Scene = "alipay_page"
	// SceneAlipayWap 支付宝手机网站支付。
	SceneAlipayWap Scene = "alipay_wap"
	// SceneAlipayNative 支付宝当面付扫码支付。
	SceneAlipayNative Scene = "alipay_native"
	// SceneAlipayApp 支付宝 APP 支付。
	SceneAlipayApp Scene = "alipay_app"
	// SceneAlipayMiniProgram 支付宝小程序支付。
	SceneAlipayMiniProgram Scene = "alipay_miniprogram"
)

// String 返回支付场景的字符串表现形式。
func (s Scene) String() string { return string(s) }

// Channel 返回该场景所属的支付渠道，未识别场景返回空值。
func (s Scene) Channel() Channel {
	switch s {
	case SceneWechatNative,
		SceneWechatJsapi,
		SceneWechatMiniProgram,
		SceneWechatH5,
		SceneWechatApp:
		return ChannelWechat
	case SceneAlipayPage,
		SceneAlipayWap,
		SceneAlipayNative,
		SceneAlipayApp,
		SceneAlipayMiniProgram:
		return ChannelAlipay
	default:
		return ""
	}
}

// IsValid 检查支付场景是否合法。
func (s Scene) IsValid() bool { return s.Channel() != "" }

// RequiresPayerIdentity 判断该场景是否必须提供付款人的渠道身份（微信 openid 或支付宝 buyer_id）。
func (s Scene) RequiresPayerIdentity() bool {
	switch s {
	case SceneWechatJsapi,
		SceneWechatMiniProgram,
		SceneAlipayMiniProgram:
		return true
	default:
		return false
	}
}

// SceneValues 获取全部支付场景枚举值。
func SceneValues() []Scene {
	return []Scene{
		SceneWechatNative,
		SceneWechatJsapi,
		SceneWechatMiniProgram,
		SceneWechatH5,
		SceneWechatApp,
		SceneAlipayPage,
		SceneAlipayWap,
		SceneAlipayNative,
		SceneAlipayApp,
		SceneAlipayMiniProgram,
	}
}

// SceneFromString 解析字符串为支付场景枚举。
func SceneFromString(value string) (Scene, error) {
	scene := Scene(value)
	if scene.IsValid() {
		return scene, nil
	}

	return "", fmt.Errorf("invalid payment scene: %s", value)
}

// InstructionAction 代表支付指令的动作枚举。
// 客户端据此决定是渲染二维码、跳转浏览器，还是用参数调起渠道 SDK。
type InstructionAction string

// 支付指令动作常量定义。
const (
	// InstructionQRCode 返回二维码链接，由客户端自行渲染。
	InstructionQRCode InstructionAction = "qr_code"
	// InstructionRedirectURL 返回收银台 URL，由客户端跳转浏览器。
	InstructionRedirectURL InstructionAction = "redirect_url"
	// InstructionInvokeParams 返回渠道二次签名后的参数集合，由客户端调起渠道 SDK。
	InstructionInvokeParams InstructionAction = "invoke_params"
)

// String 返回支付指令动作的字符串表现形式。
func (a InstructionAction) String() string { return string(a) }

// IsValid 检查支付指令动作是否合法。
func (a InstructionAction) IsValid() bool {
	switch a {
	case InstructionQRCode, InstructionRedirectURL, InstructionInvokeParams:
		return true
	default:
		return false
	}
}

// Status 代表支付状态枚举。
// 终态不可变：全额退款不改写支付状态，而是累积已退款金额并保留成功态。
type Status string

// 支付状态常量定义。
const (
	// StatusPending 已创建，等待付款人完成支付。
	StatusPending Status = "pending"
	// StatusSucceeded 支付成功（终态）。
	StatusSucceeded Status = "succeeded"
	// StatusFailed 渠道明确返回支付失败（终态）。
	StatusFailed Status = "failed"
	// StatusClosed 主动关单或超时关闭（终态）。
	StatusClosed Status = "closed"
	// StatusUnknown 渠道返回了本地未映射的状态。
	// 调用方应保持本地状态不变并告警，严禁据此推进支付结果。
	StatusUnknown Status = "unknown"
)

// String 返回支付状态的字符串表现形式。
func (s Status) String() string { return string(s) }

// IsValid 检查支付状态是否合法。
func (s Status) IsValid() bool {
	switch s {
	case StatusPending,
		StatusSucceeded,
		StatusFailed,
		StatusClosed,
		StatusUnknown:
		return true
	default:
		return false
	}
}

// IsFinal 判断支付状态是否为终态。
func (s Status) IsFinal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusClosed:
		return true
	default:
		return false
	}
}

// CanTransitionTo 判断是否允许从当前状态跳转到目标状态。
// 终态不会再次跳转，未知状态不被信任，因此二者一律拒绝。
func (s Status) CanTransitionTo(target Status) bool {
	if s.IsFinal() || s == StatusUnknown || target == StatusUnknown {
		return false
	}

	return s == StatusPending
}

// RefundStatus 代表退款状态枚举。
type RefundStatus string

// 退款状态常量定义。
const (
	// RefundStatusPending 退款已提交，渠道仍在处理。
	RefundStatusPending RefundStatus = "pending"
	// RefundStatusSucceeded 退款成功（终态）。
	RefundStatusSucceeded RefundStatus = "succeeded"
	// RefundStatusFailed 退款异常失败（终态）。
	RefundStatusFailed RefundStatus = "failed"
	// RefundStatusCanceled 退款关闭或撤销（终态）。
	RefundStatusCanceled RefundStatus = "canceled"
	// RefundStatusUnknown 渠道返回了本地未映射的退款状态。
	RefundStatusUnknown RefundStatus = "unknown"
)

// String 返回退款状态的字符串表现形式。
func (s RefundStatus) String() string { return string(s) }

// IsValid 检查退款状态是否合法。
func (s RefundStatus) IsValid() bool {
	switch s {
	case RefundStatusPending,
		RefundStatusSucceeded,
		RefundStatusFailed,
		RefundStatusCanceled,
		RefundStatusUnknown:
		return true
	default:
		return false
	}
}

// IsFinal 判断退款状态是否为终态。
func (s RefundStatus) IsFinal() bool {
	switch s {
	case RefundStatusSucceeded, RefundStatusFailed, RefundStatusCanceled:
		return true
	default:
		return false
	}
}

// Succeeded 判断退款是否已成功到账。
func (s RefundStatus) Succeeded() bool { return s == RefundStatusSucceeded }

// NotificationKind 代表渠道通知类型枚举。
// 支付通知与退款通知共用同一回调端点，凭本枚举分发。
type NotificationKind string

// 渠道通知类型常量定义。
const (
	// NotificationKindPayment 支付结果通知。
	NotificationKindPayment NotificationKind = "payment"
	// NotificationKindRefund 退款结果通知。
	NotificationKindRefund NotificationKind = "refund"
)

// String 返回渠道通知类型的字符串表现形式。
func (k NotificationKind) String() string { return string(k) }

// IsValid 检查渠道通知类型是否合法。
func (k NotificationKind) IsValid() bool {
	return k == NotificationKindPayment || k == NotificationKindRefund
}

// PayableType 代表被支付对象类型枚举。
// 支付域只接受白名单内的类型，不校验目标对象是否存在或处于何种状态。
type PayableType string

// 被支付对象类型常量定义。
const (
	// PayableTypeOrder 一次性订单。
	PayableTypeOrder PayableType = "order"
)

// String 返回被支付对象类型的字符串表现形式。
func (t PayableType) String() string { return string(t) }

// IsValid 检查被支付对象类型是否在允许集合内。
func (t PayableType) IsValid() bool { return t == PayableTypeOrder }

// PayableTypeValues 获取全部被支付对象类型枚举值。
func PayableTypeValues() []PayableType { return []PayableType{PayableTypeOrder} }

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
	Query(ctx context.Context, channel Channel, req QueryRequest, application ...string) (*PaymentResult, error)
	// Close 关闭一笔未支付的收款。若未显式指定 application，则自动使用对应渠道的默认应用。
	Close(ctx context.Context, channel Channel, req CloseRequest, application ...string) error
	// Refund 发起全额或部分退款。若未显式指定 application，则自动使用对应渠道的默认应用。
	Refund(ctx context.Context, channel Channel, req RefundRequest, application ...string) (*RefundResult, error)
	// QueryRefund 主动查询退款状态。若未显式指定 application，则自动使用对应渠道的默认应用。
	QueryRefund(ctx context.Context, channel Channel, req QueryRefundRequest, application ...string) (*RefundResult, error)
	// ParseNotification 聚合解析并验签渠道异步通知。
	ParseNotification(ctx context.Context, channel Channel, application string, req *http.Request) (*Notification, error)
	// For 按「渠道 + 应用标识」显式解析 Provider（保留高级自定义退路）。
	For(channel Channel, application string) (Provider, error)
	// ForScene 按「渠道 + 支付场景」显式解析 Provider。
	ForScene(channel Channel, scene Scene) (Provider, error)
	// Applications 返回指定渠道下已注册的应用标识。
	Applications(channel Channel) []string
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
	Channel() Channel
	// Application 返回该 Provider 绑定的渠道应用标识。
	Application() string
	// Scenes 返回该渠道应用声明可承载的支付场景。
	Scenes() []Scene

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
	Scene Scene
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
	// 仅 Scene.RequiresPayerIdentity 为真的场景必填。
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

// Instruction 描述客户端如何把付款人交给渠道完成支付。
// Action 与 Value/Params 的组合由渠道返回物归一而来：
//   - qr_code：Value 为二维码链接，由客户端自行渲染；
//   - redirect_url：Value 为收银台跳转地址；
//   - invoke_params：Params 为渠道二次签名后的参数集合。
type Instruction struct {
	// Scene 该指令对应的支付场景。
	Scene Scene
	// Action 客户端应当执行的动作。
	Action InstructionAction
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
	Status Status
	// ChannelState 渠道返回的原始状态字符串，供审计与排障使用。
	ChannelState string
	// PaidAmount 实际到账金额，单位为分。
	PaidAmount int64
	// PaidAt 支付完成时刻，未支付时为空。
	PaidAt *time.Time
	// PayerID 付款人的渠道身份。
	PayerID string
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
	Status RefundStatus
	// ChannelState 渠道返回的原始状态字符串，供审计与排障使用。
	ChannelState string
	// RefundAmount 本次退款金额，单位为分。
	RefundAmount int64
	// RefundedAt 退款成功时刻，未成功时为空。
	RefundedAt *time.Time
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
var notificationAcks = map[Channel]struct {
	Success NotificationAck
	Failure NotificationAck
}{
	ChannelWechat: {
		Success: NotificationAck{ContentType: wechatAckContentType, Body: wechatSuccessAckBody},
		Failure: NotificationAck{ContentType: wechatAckContentType, Body: wechatFailureAckBody},
	},
	ChannelAlipay: {
		Success: NotificationAck{ContentType: alipayAckContentType, Body: alipaySuccessAckBody},
		Failure: NotificationAck{ContentType: alipayAckContentType, Body: alipayFailureAckBody},
	},
}

// SuccessAck 返回指定渠道要求的通知成功应答。
func SuccessAck(channel Channel) NotificationAck {
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
func FailureAck(channel Channel) NotificationAck {
	if ack, ok := notificationAcks[channel]; ok {
		return ack.Failure
	}

	return NotificationAck{ContentType: plainTextContentType, Body: unknownChannelAckBody}
}

// Notification 是渠道异步通知的规范化结果。
type Notification struct {
	// Kind 通知类型，支付通知与退款通知共用同一回调端点。
	Kind NotificationKind
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
