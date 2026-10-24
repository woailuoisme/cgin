// Package enums 定义支付域的渠道、场景、状态与通知类型枚举。
package enums

import (
	"fmt"
)

// PaymentChannel 代表支付渠道领域枚举。
// 与认证域的 ProviderID 刻意区分：后者描述身份绑定，本枚举描述资金通道。
type PaymentChannel string

// 支付渠道常量定义。
const (
	// PaymentChannelWechat 微信支付渠道。
	PaymentChannelWechat PaymentChannel = "wechat"
	// PaymentChannelAlipay 支付宝渠道。
	PaymentChannelAlipay PaymentChannel = "alipay"
)

// String 返回支付渠道的字符串表现形式。
func (c PaymentChannel) String() string { return string(c) }

// IsValid 检查支付渠道是否合法。
func (c PaymentChannel) IsValid() bool {
	return c == PaymentChannelWechat || c == PaymentChannelAlipay
}

// PaymentChannelValues 获取全部支付渠道枚举值。
func PaymentChannelValues() []PaymentChannel {
	return []PaymentChannel{PaymentChannelWechat, PaymentChannelAlipay}
}

// PaymentScene 代表支付场景领域枚举，即渠道内的具体交互方式。
// 场景决定使用哪个渠道应用，以及付款人必须提供哪些参数，因此属于支付请求的一部分。
type PaymentScene string

// 支付场景常量定义。取值以「渠道_方式」前缀区分，便于日志检索与排障。
const (
	// PaymentSceneWechatNative 微信 Native 扫码支付。
	PaymentSceneWechatNative PaymentScene = "wechat_native"
	// PaymentSceneWechatJsapi 微信公众号 JSAPI 支付。
	PaymentSceneWechatJsapi PaymentScene = "wechat_jsapi"
	// PaymentSceneWechatMiniProgram 微信小程序支付。
	PaymentSceneWechatMiniProgram PaymentScene = "wechat_miniprogram"
	// PaymentSceneWechatH5 微信 H5 支付。
	PaymentSceneWechatH5 PaymentScene = "wechat_h5"
	// PaymentSceneWechatApp 微信 APP 支付。
	PaymentSceneWechatApp PaymentScene = "wechat_app"
	// PaymentSceneAlipayPage 支付宝电脑网站支付。
	PaymentSceneAlipayPage PaymentScene = "alipay_page"
	// PaymentSceneAlipayWap 支付宝手机网站支付。
	PaymentSceneAlipayWap PaymentScene = "alipay_wap"
	// PaymentSceneAlipayNative 支付宝当面付扫码支付。
	PaymentSceneAlipayNative PaymentScene = "alipay_native"
	// PaymentSceneAlipayApp 支付宝 APP 支付。
	PaymentSceneAlipayApp PaymentScene = "alipay_app"
	// PaymentSceneAlipayMiniProgram 支付宝小程序支付。
	PaymentSceneAlipayMiniProgram PaymentScene = "alipay_miniprogram"
)

// String 返回支付场景的字符串表现形式。
func (s PaymentScene) String() string { return string(s) }

// Channel 返回该场景所属的支付渠道，未识别场景返回空值。
func (s PaymentScene) Channel() PaymentChannel {
	switch s {
	case PaymentSceneWechatNative,
		PaymentSceneWechatJsapi,
		PaymentSceneWechatMiniProgram,
		PaymentSceneWechatH5,
		PaymentSceneWechatApp:
		return PaymentChannelWechat
	case PaymentSceneAlipayPage,
		PaymentSceneAlipayWap,
		PaymentSceneAlipayNative,
		PaymentSceneAlipayApp,
		PaymentSceneAlipayMiniProgram:
		return PaymentChannelAlipay
	default:
		return ""
	}
}

// IsValid 检查支付场景是否合法。
func (s PaymentScene) IsValid() bool { return s.Channel() != "" }

// RequiresPayerIdentity 判断该场景是否必须提供付款人的渠道身份（微信 openid 或支付宝 buyer_id）。
func (s PaymentScene) RequiresPayerIdentity() bool {
	switch s {
	case PaymentSceneWechatJsapi,
		PaymentSceneWechatMiniProgram,
		PaymentSceneAlipayMiniProgram:
		return true
	default:
		return false
	}
}

// PaymentSceneValues 获取全部支付场景枚举值。
func PaymentSceneValues() []PaymentScene {
	return []PaymentScene{
		PaymentSceneWechatNative,
		PaymentSceneWechatJsapi,
		PaymentSceneWechatMiniProgram,
		PaymentSceneWechatH5,
		PaymentSceneWechatApp,
		PaymentSceneAlipayPage,
		PaymentSceneAlipayWap,
		PaymentSceneAlipayNative,
		PaymentSceneAlipayApp,
		PaymentSceneAlipayMiniProgram,
	}
}

// PaymentSceneFromString 解析字符串为支付场景枚举。
func PaymentSceneFromString(value string) (PaymentScene, error) {
	scene := PaymentScene(value)
	if scene.IsValid() {
		return scene, nil
	}

	return "", fmt.Errorf("invalid payment scene: %s", value)
}

// PaymentInstructionAction 代表支付指令的动作枚举。
// 客户端据此决定是渲染二维码、跳转浏览器，还是用参数调起渠道 SDK。
type PaymentInstructionAction string

// 支付指令动作常量定义。
const (
	// PaymentInstructionQRCode 返回二维码链接，由客户端自行渲染。
	PaymentInstructionQRCode PaymentInstructionAction = "qr_code"
	// PaymentInstructionRedirectURL 返回收银台 URL，由客户端跳转浏览器。
	PaymentInstructionRedirectURL PaymentInstructionAction = "redirect_url"
	// PaymentInstructionInvokeParams 返回渠道二次签名后的参数集合，由客户端调起渠道 SDK。
	PaymentInstructionInvokeParams PaymentInstructionAction = "invoke_params"
)

// String 返回支付指令动作的字符串表现形式。
func (a PaymentInstructionAction) String() string { return string(a) }

// IsValid 检查支付指令动作是否合法。
func (a PaymentInstructionAction) IsValid() bool {
	switch a {
	case PaymentInstructionQRCode, PaymentInstructionRedirectURL, PaymentInstructionInvokeParams:
		return true
	default:
		return false
	}
}

// PaymentStatus 代表支付状态枚举。
// 终态不可变：全额退款不改写支付状态，而是累积已退款金额并保留成功态。
type PaymentStatus string

// 支付状态常量定义。
const (
	// PaymentStatusPending 已创建，等待付款人完成支付。
	PaymentStatusPending PaymentStatus = "pending"
	// PaymentStatusSucceeded 支付成功（终态）。
	PaymentStatusSucceeded PaymentStatus = "succeeded"
	// PaymentStatusFailed 渠道明确返回支付失败（终态）。
	PaymentStatusFailed PaymentStatus = "failed"
	// PaymentStatusClosed 主动关单或超时关闭（终态）。
	PaymentStatusClosed PaymentStatus = "closed"
	// PaymentStatusUnknown 渠道返回了本地未映射的状态。
	// 调用方应保持本地状态不变并告警，严禁据此推进支付结果。
	PaymentStatusUnknown PaymentStatus = "unknown"
)

// 通用展示元数据常量（用于支付状态与退款状态徽标展示）。
const (
	colorSecondary = "secondary"
	colorSuccess   = "success"
	colorDanger    = "danger"
	colorWarning   = "warning"
	colorGray      = "gray"
	labelPending   = "Pending"
)

// paymentStatusMeta 存储支付状态的展示元数据。
var paymentStatusMeta = map[PaymentStatus]struct {
	Label    string
	Color    string
	Priority int
}{
	PaymentStatusPending:   {Label: labelPending, Color: colorWarning, Priority: 1},
	PaymentStatusSucceeded: {Label: "Succeeded", Color: colorSuccess, Priority: 2},
	PaymentStatusFailed:    {Label: "Failed", Color: colorDanger, Priority: 3},
	PaymentStatusClosed:    {Label: "Closed", Color: colorSecondary, Priority: 4},
	PaymentStatusUnknown:   {Label: "Unknown", Color: colorGray, Priority: 99},
}

// String 返回支付状态的字符串表现形式。
func (s PaymentStatus) String() string {
	if meta, ok := paymentStatusMeta[s]; ok {
		return meta.Label
	}

	return string(s)
}

// Label 获取支付状态的可读文本标签。
func (s PaymentStatus) Label() string { return s.String() }

// Color 获取支付状态对应的徽标颜色。
func (s PaymentStatus) Color() string {
	if meta, ok := paymentStatusMeta[s]; ok {
		return meta.Color
	}

	return colorGray
}

// Priority 获取支付状态排序优先级。
func (s PaymentStatus) Priority() int {
	if meta, ok := paymentStatusMeta[s]; ok {
		return meta.Priority
	}

	return 999
}

// IsValid 检查支付状态是否合法。
func (s PaymentStatus) IsValid() bool { _, ok := paymentStatusMeta[s]; return ok }

// IsFinal 判断支付状态是否为终态。
func (s PaymentStatus) IsFinal() bool {
	switch s {
	case PaymentStatusSucceeded, PaymentStatusFailed, PaymentStatusClosed:
		return true
	default:
		return false
	}
}

// CanTransitionTo 判断是否允许从当前状态跳转到目标状态。
// 终态不会再次跳转，未知状态不被信任，因此二者一律拒绝。
func (s PaymentStatus) CanTransitionTo(target PaymentStatus) bool {
	if s.IsFinal() || s == PaymentStatusUnknown || target == PaymentStatusUnknown {
		return false
	}

	return s == PaymentStatusPending
}

// PaymentRefundStatus 代表退款状态枚举。
type PaymentRefundStatus string

// 退款状态常量定义。
const (
	// PaymentRefundStatusPending 退款已提交，渠道仍在处理。
	PaymentRefundStatusPending PaymentRefundStatus = "pending"
	// PaymentRefundStatusSucceeded 退款成功（终态）。
	PaymentRefundStatusSucceeded PaymentRefundStatus = "succeeded"
	// PaymentRefundStatusFailed 退款异常失败（终态）。
	PaymentRefundStatusFailed PaymentRefundStatus = "failed"
	// PaymentRefundStatusCanceled 退款关闭或撤销（终态）。
	PaymentRefundStatusCanceled PaymentRefundStatus = "canceled"
	// PaymentRefundStatusUnknown 渠道返回了本地未映射的退款状态。
	PaymentRefundStatusUnknown PaymentRefundStatus = "unknown"
)

// paymentRefundStatusMeta 存储退款状态的展示元数据。
var paymentRefundStatusMeta = map[PaymentRefundStatus]struct {
	Label    string
	Color    string
	Priority int
}{
	PaymentRefundStatusPending:   {Label: labelPending, Color: colorWarning, Priority: 1},
	PaymentRefundStatusSucceeded: {Label: "Succeeded", Color: colorSuccess, Priority: 2},
	PaymentRefundStatusFailed:    {Label: "Failed", Color: colorDanger, Priority: 3},
	PaymentRefundStatusCanceled:  {Label: "Canceled", Color: colorSecondary, Priority: 4},
	PaymentRefundStatusUnknown:   {Label: "Unknown", Color: colorGray, Priority: 99},
}

// String 返回退款状态的字符串表现形式。
func (s PaymentRefundStatus) String() string {
	if meta, ok := paymentRefundStatusMeta[s]; ok {
		return meta.Label
	}

	return string(s)
}

// Label 获取退款状态的可读文本标签。
func (s PaymentRefundStatus) Label() string { return s.String() }

// Color 获取退款状态对应的徽标颜色。
func (s PaymentRefundStatus) Color() string {
	if meta, ok := paymentRefundStatusMeta[s]; ok {
		return meta.Color
	}

	return colorGray
}

// Priority 获取退款状态排序优先级。
func (s PaymentRefundStatus) Priority() int {
	if meta, ok := paymentRefundStatusMeta[s]; ok {
		return meta.Priority
	}

	return 999
}

// IsValid 检查退款状态是否合法。
func (s PaymentRefundStatus) IsValid() bool { _, ok := paymentRefundStatusMeta[s]; return ok }

// IsFinal 判断退款状态是否为终态。
func (s PaymentRefundStatus) IsFinal() bool {
	switch s {
	case PaymentRefundStatusSucceeded, PaymentRefundStatusFailed, PaymentRefundStatusCanceled:
		return true
	default:
		return false
	}
}

// Succeeded 判断退款是否已成功到账。
func (s PaymentRefundStatus) Succeeded() bool { return s == PaymentRefundStatusSucceeded }

// PaymentNotificationKind 代表渠道通知类型枚举。
// 支付通知与退款通知共用同一回调端点，凭本枚举分发。
type PaymentNotificationKind string

// 渠道通知类型常量定义。
const (
	// PaymentNotificationKindPayment 支付结果通知。
	PaymentNotificationKindPayment PaymentNotificationKind = "payment"
	// PaymentNotificationKindRefund 退款结果通知。
	PaymentNotificationKindRefund PaymentNotificationKind = "refund"
)

// String 返回渠道通知类型的字符串表现形式。
func (k PaymentNotificationKind) String() string { return string(k) }

// IsValid 检查渠道通知类型是否合法。
func (k PaymentNotificationKind) IsValid() bool {
	return k == PaymentNotificationKindPayment || k == PaymentNotificationKindRefund
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
