package notification_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/mailer"
	"bgin/internal/platform/notification/contract"
	sharedNotif "bgin/internal/shared/notification"
)

func TestSecurityAlertNotification(t *testing.T) {
	t.Parallel()

	n := sharedNotif.NewSecurityAlertNotification(
		"Suspicious Login",
		"Your account was logged in from a new IP",
		"192.168.1.100",
		"Mozilla/5.0",
	)

	// 1. 验证渠道
	assert.ElementsMatch(t, []string{"database", "mail", "realtime"}, n.Via(nil))

	// 2. 验证邮件载荷
	mailObj := n.ToMail(nil)
	mailMsg, ok := mailObj.(*mailer.Message)
	require.True(t, ok)
	assert.Equal(t, "Suspicious Login", mailMsg.Subject)
	assert.Equal(t, "Your account was logged in from a new IP", mailMsg.Body)

	// 3. 验证数据库载荷
	dbMap := n.ToDatabase(nil)
	assert.Equal(t, "Suspicious Login", dbMap["title"])
	assert.Equal(t, "192.168.1.100", dbMap["ip_address"])
	assert.Equal(t, "Mozilla/5.0", dbMap["user_agent"])

	// 4. 验证默认实时广播载荷
	rtMsgDefault := n.ToRealtime(nil)
	assert.Equal(t, "notifications", rtMsgDefault.Channel)
	assert.Equal(t, "security_alert", rtMsgDefault.Event)

	// 5. 验证指定接收者的自定义实时广播频道路由
	recipient := contract.NewRecipient("user_999", "users").WithRoute("realtime", "private:users.user_999")
	rtMsgCustom := n.ToRealtime(recipient)
	assert.Equal(t, "private:users.user_999", rtMsgCustom.Channel)
	assert.Equal(t, "security_alert", rtMsgCustom.Event)
}
