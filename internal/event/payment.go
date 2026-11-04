package event

import (
	"time"

	"cgin/pkg/payment"
)

// PaymentSucceededEvent 在支付渠道异步通知验签通过且确认收款成功后广播。
type PaymentSucceededEvent struct {
	Channel   payment.Channel        `json:"channel"`
	Payment   *payment.PaymentResult `json:"payment"`
	Timestamp time.Time              `json:"timestamp"`
}

// EventName 实现 eventbus.Event 契约。
func (PaymentSucceededEvent) EventName() string {
	return "payment.succeeded"
}

// PaymentRefundedEvent 在退款渠道异步通知验签通过且确认退款后广播。
type PaymentRefundedEvent struct {
	Channel   payment.Channel       `json:"channel"`
	Refund    *payment.RefundResult `json:"refund"`
	Timestamp time.Time             `json:"timestamp"`
}

// EventName 实现 eventbus.Event 契约。
func (PaymentRefundedEvent) EventName() string {
	return "payment.refunded"
}
