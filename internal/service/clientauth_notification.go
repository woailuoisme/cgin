package service

import (
	"cmp"
	"strings"
	"time"

	"cgin/pkg/notification"
	"cgin/pkg/notification/contract"
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

// 静态断言通知契约实现。
var (
	_ contract.Notification         = (*AuthOTPNotification)(nil)
	_ notification.MailNotification = (*AuthOTPNotification)(nil)
	_ contract.Notification         = (*PhoneOTPNotification)(nil)
	_ notification.SMSNotification  = (*PhoneOTPNotification)(nil)
)
