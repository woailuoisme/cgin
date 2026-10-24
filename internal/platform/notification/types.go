// Package notification 提供统一、模块化、高可扩展的多渠道通知基础设施。
package notification

import (
	"bgin/internal/platform/notification/contract"
)

const (
	// DefaultNotifiableType 默认接收者实体类型。
	DefaultNotifiableType = contract.DefaultNotifiableType
)

// 重新导出核心契约接口与类型定义。
type (
	Notifiable              = contract.Notifiable
	Recipient               = contract.Recipient
	Notification            = contract.Notification
	ChannelFormatter        = contract.ChannelFormatter
	Channel                 = contract.Channel
	Formatter               = contract.Formatter
	IdempotentNotification  = contract.IdempotentNotification
	RateLimitedNotification = contract.RateLimitedNotification
	ShouldQueue             = contract.ShouldQueue
	ShouldQueueWithOptions  = contract.ShouldQueueWithOptions
	SendOption              = contract.SendOption
	SendOptions             = contract.SendOptions
)

// 重新导出标准哨兵错误、工厂方法与选项函数。
var (
	NewRecipient = contract.NewRecipient
	Route        = contract.Route
	ToNotifiable = contract.ToNotifiable

	ErrNoChannel      = contract.ErrNoChannel
	ErrInvalidMessage = contract.ErrInvalidMessage
	ErrNoRoute        = contract.ErrNoRoute
	ErrNoFormatter    = contract.ErrNoFormatter
	ErrClosed         = contract.ErrClosed
	ErrRateLimited    = contract.ErrRateLimited
	ErrDeduplicated   = contract.ErrDeduplicated

	Via   = contract.Via
	Sync  = contract.Sync
	Async = contract.Async
)
