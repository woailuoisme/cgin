package realtime_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	featureRealtime "bgin/internal/features/realtime"
	platformRealtime "bgin/internal/platform/realtime"
)

func TestChannelHelpers(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "private:users.u123", featureRealtime.UserChannel("u123"))
	assert.Equal(t, "personal#u123", featureRealtime.PersonalChannel("u123"))
	assert.Equal(t, "admin:notifications", featureRealtime.AdminNotificationChannel())
}

func TestRegisterChannelsAuthorization(t *testing.T) {
	t.Parallel()

	reg := platformRealtime.NewRegistry()
	featureRealtime.RegisterChannels(reg, nil)

	// 1. 验证个人用户私有频道 private:users.{id}
	t.Run("用户订阅自身私有频道", func(t *testing.T) {
		err := reg.Authorize(t.Context(), platformRealtime.User{ID: "usr_alice"}, "private:users.usr_alice")
		require.NoError(t, err)
	})

	t.Run("用户尝试订阅他人私有频道被拒", func(t *testing.T) {
		err := reg.Authorize(t.Context(), platformRealtime.User{ID: "usr_alice"}, "private:users.usr_bob")
		require.Error(t, err)
	})

	t.Run("匿名用户订阅私有频道被拒", func(t *testing.T) {
		err := reg.Authorize(t.Context(), platformRealtime.User{ID: ""}, "private:users.usr_bob")
		require.Error(t, err)
	})

	// 2. 验证公共频道 public:*
	t.Run("登录用户订阅公共频道", func(t *testing.T) {
		err := reg.Authorize(t.Context(), platformRealtime.User{ID: "usr_anyone"}, "public:announcements")
		require.NoError(t, err)
	})

	t.Run("未登录用户订阅公共频道被拒", func(t *testing.T) {
		err := reg.Authorize(t.Context(), platformRealtime.User{ID: ""}, "public:announcements")
		require.Error(t, err)
	})

	// 3. 验证管理员频道鉴权与 Fail-Closed 安全性
	t.Run("未登录用户订阅管理员频道被拒", func(t *testing.T) {
		err := reg.Authorize(t.Context(), platformRealtime.User{ID: ""}, "admin:notifications")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "authentication required")
	})

	t.Run("未注入用户仓库时管理员频道遵循 Fail-Closed 默认拒绝", func(t *testing.T) {
		err := reg.Authorize(t.Context(), platformRealtime.User{ID: "usr_admin"}, "admin:notifications")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "authorization service unavailable")
	})

	// 4. 验证幂等性
	featureRealtime.RegisterChannels(reg, nil)
}
