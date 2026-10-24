package notification

import (
	"cmp"

	"bgin/internal/platform/mailer"
	"bgin/internal/platform/notification"
	"bgin/internal/platform/notification/contract"
	sharedDatabase "bgin/internal/shared/notification/database"
)

// SecurityAlertNotification 多渠道账号安全事件通知（邮件 + 数据库站内信 + 实时广播）。
type SecurityAlertNotification struct {
	Title     string
	Message   string
	IPAddress string
	UserAgent string
}

// NewSecurityAlertNotification 创建安全提醒通知。
func NewSecurityAlertNotification(title, message, ip, ua string) *SecurityAlertNotification {
	return &SecurityAlertNotification{
		Title:     title,
		Message:   message,
		IPAddress: ip,
		UserAgent: ua,
	}
}

// Via 声明多渠道投递目标（数据库站内信 + 邮件 + 实时广播）。
func (n *SecurityAlertNotification) Via(_ any) []string {
	return []string{"database", "mail", "realtime"}
}

// ToMail 生成安全警报邮件载荷（实现 notification.MailNotification）。
func (n *SecurityAlertNotification) ToMail(_ any) any {
	return &mailer.Message{
		Subject: n.Title,
		Body:    n.Message,
		IsHTML:  true,
	}
}

// ToDatabase 生成入库持久化数据（实现 notification.DatabaseNotification）。
func (n *SecurityAlertNotification) ToDatabase(_ any) map[string]any {
	return map[string]any{
		"title":      n.Title,
		"message":    n.Message,
		"ip_address": n.IPAddress,
		"user_agent": n.UserAgent,
	}
}

// ToRealtime 生成 Centrifugo / WebSocket 实时广播数据（实现 notification.RealtimeNotification）。
func (n *SecurityAlertNotification) ToRealtime(notifiable any) notification.RealtimeMessage {
	channel := "notifications"
	if notif, ok := notifiable.(contract.Notifiable); ok {
		channel = cmp.Or(notif.RouteNotificationFor("realtime"), channel)
	}
	return notification.RealtimeMessage{
		Channel: channel,
		Event:   "security_alert",
		Data: map[string]any{
			"title":   n.Title,
			"message": n.Message,
		},
	}
}

// 静态断言通知契约实现。
var (
	_ contract.Notification             = (*SecurityAlertNotification)(nil)
	_ notification.MailNotification     = (*SecurityAlertNotification)(nil)
	_ sharedDatabase.Notification       = (*SecurityAlertNotification)(nil)
	_ notification.RealtimeNotification = (*SecurityAlertNotification)(nil)
)
