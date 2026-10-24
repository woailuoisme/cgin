package events

import (
	"time"

	platformPayment "bgin/internal/platform/payment"
	"bgin/internal/platform/payment/enums"
)

// PaymentSucceededEvent 在支付渠道异步通知验签通过且确认收款成功后广播。
type PaymentSucceededEvent struct {
	Channel   enums.PaymentChannel           `json:"channel"`
	Payment   *platformPayment.PaymentResult `json:"payment"`
	Timestamp time.Time                      `json:"timestamp"`
}

// EventName 实现 eventbus.Event 契约。
func (PaymentSucceededEvent) EventName() string {
	return "payment.succeeded"
}

// PaymentRefundedEvent 在退款渠道异步通知验签通过且确认退款后广播。
type PaymentRefundedEvent struct {
	Channel   enums.PaymentChannel          `json:"channel"`
	Refund    *platformPayment.RefundResult `json:"refund"`
	Timestamp time.Time                     `json:"timestamp"`
}

// EventName 实现 eventbus.Event 契约。
func (PaymentRefundedEvent) EventName() string {
	return "payment.refunded"
}
