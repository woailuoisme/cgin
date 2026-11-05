package clientauth

import (
	"cmp"
	"strings"
	"time"

	"cgin/internal/feature/client/auth/store"
	"cgin/pkg/mailer"
	"cgin/pkg/notification"
)

const defaultAuthOTPExpiry = 10 * time.Minute

// authOTPTemplate 生成认证类验证码邮件的模板名与变量。
func authOTPTemplate(name, code string, expiresIn time.Duration) (string, map[string]any) {
	return "auth_otp", map[string]any{
		"Name":      cmp.Or(strings.TrimSpace(name), "User"),
		"Code":      code,
		"ExpiresIn": int(expiresIn.Minutes()),
	}
}

// AuthOTPNotification 是注册验证与找回密码共用的邮箱验证码通知。
type AuthOTPNotification struct {
	Name        string
	Code        string
	SubjectText string
	ExpiresIn   time.Duration
}

// NewSignupVerificationNotification 创建注册邮箱验证码通知。
func NewSignupVerificationNotification(name, code string) *AuthOTPNotification {
	return &AuthOTPNotification{
		Name:        name,
		Code:        code,
		SubjectText: "Verify your email",
		ExpiresIn:   defaultAuthOTPExpiry,
	}
}

// NewPasswordResetNotification 创建找回密码验证码通知。
func NewPasswordResetNotification(name, code string) *AuthOTPNotification {
	return &AuthOTPNotification{
		Name:        name,
		Code:        code,
		SubjectText: "Password reset verification code",
		ExpiresIn:   defaultAuthOTPExpiry,
	}
}

// Via 声明发送渠道为邮件。
func (n *AuthOTPNotification) Via(_ any) []string {
	return []string{"mail"}
}

// Subject 返回认证邮件标题。
func (n *AuthOTPNotification) Subject() string {
	return n.SubjectText
}

// Template 返回认证邮件模板名称与变量（实现 mailer.Mailable）。
func (n *AuthOTPNotification) Template() (string, map[string]any) {
	return authOTPTemplate(n.Name, n.Code, n.ExpiresIn)
}

// ToMail 生成邮件格式内容（实现 notification.MailNotification）。
func (n *AuthOTPNotification) ToMail(_ any) any {
	return n
}

// PhoneOTPNotification 手机短信验证码通知。
type PhoneOTPNotification struct {
	PhoneNumber string
	Code        string
	ExpiresIn   time.Duration
}

// NewPhoneOTPNotification 创建手机短信验证码通知。
func NewPhoneOTPNotification(phoneNumber, code string) *PhoneOTPNotification {
	return &PhoneOTPNotification{
		PhoneNumber: phoneNumber,
		Code:        code,
		ExpiresIn:   defaultAuthOTPExpiry,
	}
}

// Via 声明发送渠道为短信。
func (n *PhoneOTPNotification) Via(_ any) []string {
	return []string{"sms"}
}

// ToSMS 生成短信格式载荷（实现 notification.SMSNotification）。
func (n *PhoneOTPNotification) ToSMS(_ any) any {
	return notification.NewSMSMessage().
		WithPhone(n.PhoneNumber).
		WithParam("code", n.Code).
		WithParam("otp", n.Code).
		WithContent(n.Code)
}

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
	if notif, ok := notifiable.(notification.Notifiable); ok {
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
	_ notification.Notification         = (*AuthOTPNotification)(nil)
	_ notification.MailNotification     = (*AuthOTPNotification)(nil)
	_ notification.Notification         = (*PhoneOTPNotification)(nil)
	_ notification.SMSNotification      = (*PhoneOTPNotification)(nil)
	_ notification.Notification         = (*SecurityAlertNotification)(nil)
	_ notification.MailNotification     = (*SecurityAlertNotification)(nil)
	_ store.Notification                = (*SecurityAlertNotification)(nil)
	_ notification.RealtimeNotification = (*SecurityAlertNotification)(nil)
)
