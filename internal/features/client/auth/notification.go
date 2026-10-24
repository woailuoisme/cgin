package auth

import (
	"cmp"
	"strings"
	"time"

	"bgin/internal/platform/notification"
	"bgin/internal/platform/notification/contract"
)

const defaultAuthOTPExpiry = 10 * time.Minute

// authOTPTemplate 生成认证类验证码邮件的模板名与变量，供注册与找回密码复用。
func authOTPTemplate(name, code string, expiresIn time.Duration) (string, map[string]any) {
	return "auth_otp", map[string]any{
		"Name":      cmp.Or(strings.TrimSpace(name), "User"),
		"Code":      code,
		"ExpiresIn": int(expiresIn.Minutes()),
	}
}

// SignupVerificationNotification 用户注册邮箱激活验证码通知。
type SignupVerificationNotification struct {
	Name      string
	Code      string
	ExpiresIn time.Duration
}

// NewSignupVerificationNotification 创建注册邮箱验证码通知。
func NewSignupVerificationNotification(name, code string) *SignupVerificationNotification {
	return &SignupVerificationNotification{
		Name:      name,
		Code:      code,
		ExpiresIn: defaultAuthOTPExpiry,
	}
}

// Via 声明发送渠道为邮件。
func (n *SignupVerificationNotification) Via(_ any) []string {
	return []string{"mail"}
}

// Subject 返回注册邮件标题。
func (n *SignupVerificationNotification) Subject() string {
	return "Verify your email"
}

// Template 返回注册邮件模板名称与变量（实现 mailer.Mailable）。
func (n *SignupVerificationNotification) Template() (string, map[string]any) {
	return authOTPTemplate(n.Name, n.Code, n.ExpiresIn)
}

// ToMail 生成邮件格式内容（实现 notification.MailNotification）。
func (n *SignupVerificationNotification) ToMail(_ any) any {
	return n
}

// PasswordResetNotification 邮箱找回密码验证码通知。
type PasswordResetNotification struct {
	Name      string
	Code      string
	ExpiresIn time.Duration
}

// NewPasswordResetNotification 创建找回密码通知。
func NewPasswordResetNotification(name, code string) *PasswordResetNotification {
	return &PasswordResetNotification{
		Name:      name,
		Code:      code,
		ExpiresIn: defaultAuthOTPExpiry,
	}
}

// Via 声明发送渠道为邮件。
func (n *PasswordResetNotification) Via(_ any) []string {
	return []string{"mail"}
}

// Subject 返回找回密码邮件标题。
func (n *PasswordResetNotification) Subject() string {
	return "Password reset verification code"
}

// Template 返回找回密码邮件模板名称与变量（实现 mailer.Mailable）。
func (n *PasswordResetNotification) Template() (string, map[string]any) {
	return authOTPTemplate(n.Name, n.Code, n.ExpiresIn)
}

// ToMail 生成邮件格式内容（实现 notification.MailNotification）。
func (n *PasswordResetNotification) ToMail(_ any) any {
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
	_ contract.Notification         = (*SignupVerificationNotification)(nil)
	_ notification.MailNotification = (*SignupVerificationNotification)(nil)
	_ contract.Notification         = (*PasswordResetNotification)(nil)
	_ notification.MailNotification = (*PasswordResetNotification)(nil)
	_ contract.Notification         = (*PhoneOTPNotification)(nil)
	_ notification.SMSNotification  = (*PhoneOTPNotification)(nil)
)
