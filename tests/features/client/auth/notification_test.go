package auth_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/features/client/auth"
	"bgin/internal/platform/notification"
)

func TestSignupVerificationNotification(t *testing.T) {
	t.Parallel()

	n := auth.NewSignupVerificationNotification("Alice", "123456")
	assert.Equal(t, []string{"mail"}, n.Via(nil))
	assert.Equal(t, "Verify your email", n.Subject())

	tmpl, data := n.Template()
	assert.Equal(t, "auth_otp", tmpl)
	assert.Equal(t, "Alice", data["Name"])
	assert.Equal(t, "123456", data["Code"])
	assert.Equal(t, 10, data["ExpiresIn"])

	mailObj := n.ToMail(nil)
	require.NotNil(t, mailObj)
}

func TestPasswordResetNotification(t *testing.T) {
	t.Parallel()

	n := auth.NewPasswordResetNotification("Bob", "654321")
	assert.Equal(t, []string{"mail"}, n.Via(nil))
	assert.Equal(t, "Password reset verification code", n.Subject())

	tmpl, data := n.Template()
	assert.Equal(t, "auth_otp", tmpl)
	assert.Equal(t, "Bob", data["Name"])
	assert.Equal(t, "654321", data["Code"])

	mailObj := n.ToMail(nil)
	require.NotNil(t, mailObj)
}

func TestPhoneOTPNotification(t *testing.T) {
	t.Parallel()

	n := auth.NewPhoneOTPNotification("+8613800138000", "888888")
	assert.Equal(t, []string{"sms"}, n.Via(nil))

	smsObj := n.ToSMS(nil)
	require.NotNil(t, smsObj)

	smsMsg, ok := smsObj.(*notification.SMSMessage)
	require.True(t, ok)
	assert.Equal(t, "+8613800138000", smsMsg.PhoneNumber)
	assert.Equal(t, "888888", smsMsg.Content)
	assert.Equal(t, "888888", smsMsg.TemplateParam["code"])
}

func TestNotificationTimeouts(t *testing.T) {
	t.Parallel()

	n := auth.NewSignupVerificationNotification("", "999999")
	n.ExpiresIn = 5 * time.Minute

	_, data := n.Template()
	assert.Equal(t, "User", data["Name"]) // 空名兜底
	assert.Equal(t, 5, data["ExpiresIn"])
}
