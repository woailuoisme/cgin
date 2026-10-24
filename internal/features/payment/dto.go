package payment

// PaymentNotifyPathParams 是 POST /payments/notify/:channel 及 /payments/notify/:channel/:app 的路径参数。
type PaymentNotifyPathParams struct {
	// 支付渠道 (wechat, alipay)
	Channel string `uri:"channel" json:"channel" validate:"required" example:"wechat"`
	// 应用标识 (可选，缺省时使用渠道默认应用)
	App string `uri:"app" json:"app,omitempty" example:"default"`
}

// AlipayReturnQueryParams 描述支付宝同步跳转 (return_url) 携带的查询参数。
type AlipayReturnQueryParams struct {
	// 商户订单号
	OutTradeNo string `form:"out_trade_no" json:"outTradeNo" example:"ORDER_20260927_001"`
	// 支付宝交易凭证号
	TradeNo string `form:"trade_no" json:"tradeNo" example:"2026092722001400000000000000"`
	// 交易金额 (元)
	TotalAmount string `form:"total_amount" json:"totalAmount" example:"88.00"`
}

// CallbackAckResource 描述支付网关向渠道返回的应答状态。
type CallbackAckResource struct {
	// 应答状态码或纯文本
	Status string `json:"status" example:"success"`
	// 应答说明
	Message string `json:"message" example:"OK"`
}

type (
	AlipayReturnQuery   = AlipayReturnQueryParams
	CallbackAckResponse = CallbackAckResource
)
