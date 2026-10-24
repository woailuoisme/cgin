package user_test

import (
	"context"
	"testing"
	"time"

	"github.com/aarondl/opt/omitnull"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/shared/enums"
	"bgin/internal/shared/user"
)

// TestReconcileBanState 验证封禁布尔状态与账号状态的协同一致性。
func TestReconcileBanState(t *testing.T) {
	t.Run("状态为banned时自动设置banned为true", func(t *testing.T) {
		fields := map[string]any{
			"status": string(enums.StatusBanned),
		}
		user.ReconcileBanState(fields)
		assert.Equal(t, true, fields["banned"])
	})

	t.Run("banned为true时自动将状态改为banned", func(t *testing.T) {
		fields := map[string]any{
			"banned": true,
			"status": string(enums.StatusActive),
		}
		user.ReconcileBanState(fields)
		assert.Equal(t, string(enums.StatusBanned), fields["status"])
	})

	t.Run("banned为false且状态为active时保持不变", func(t *testing.T) {
		fields := map[string]any{
			"banned": false,
			"status": string(enums.StatusActive),
		}
		user.ReconcileBanState(fields)
		assert.Equal(t, false, fields["banned"])
		assert.Equal(t, string(enums.StatusActive), fields["status"])
	})
}

// TestSetRequiredString 验证必填枚举与字符串列在三态更新下的校验契约。
func TestSetRequiredString(t *testing.T) {
	t.Run("未传递值时保持不变", func(t *testing.T) {
		fields := make(map[string]any)
		err := user.SetRequiredString(fields, "role", omitnull.Val[enums.Role]{})
		require.NoError(t, err)
		assert.Empty(t, fields)
	})

	t.Run("显式传入null时返回必填字段不可为null错误", func(t *testing.T) {
		fields := make(map[string]any)
		err := user.SetRequiredString(fields, "role", omitnull.FromPtr[enums.Role](nil))
		require.Error(t, err)
		assert.Empty(t, fields)
	})

	t.Run("传入有效值时写入待更新映射", func(t *testing.T) {
		fields := make(map[string]any)
		err := user.SetRequiredString(fields, "role", omitnull.From(enums.RoleAdmin))
		require.NoError(t, err)
		assert.Equal(t, string(enums.RoleAdmin), fields["role"])
	})
}

// TestNullableTime 验证时间字段的空值与UTC归一化转换。
func TestNullableTime(t *testing.T) {
	t.Run("null值转换为nil", func(t *testing.T) {
		val := user.NullableTime(omitnull.FromPtr[time.Time](nil))
		assert.Nil(t, val)
	})

	t.Run("非空时间统一归一到UTC", func(t *testing.T) {
		cst := time.FixedZone("CST", 8*3600)
		now := time.Date(2026, 10, 1, 12, 0, 0, 0, cst)
		val := user.NullableTime(omitnull.From(now))
		resTime, ok := val.(time.Time)
		require.True(t, ok)
		assert.Equal(t, time.UTC, resTime.Location())
		assert.Equal(t, 4, resTime.Hour()) // 12:00 CST -> 04:00 UTC
	})
}

// TestCollectAdminFields 验证管理端字段集的收集与协同逻辑。
func TestCollectAdminFields(t *testing.T) {
	ctx := context.Background()

	t.Run("收集全部管理端字段并执行状态协同", func(t *testing.T) {
		patch := user.AdminFieldSet{
			FieldSet: user.FieldSet{
				Name: omitnull.From("Admin User"),
			},
			EmailVerified: omitnull.From(true),
			Role:          omitnull.From(enums.RoleAdmin),
			Status:        omitnull.From(enums.StatusActive),
			Banned:        omitnull.From(false),
			BanReason:     omitnull.From("Test reason"),
		}

		fields, err := user.CollectAdminFields(ctx, nil, "usr_123", patch)
		require.NoError(t, err)
		assert.Equal(t, "Admin User", fields["name"])
		assert.Equal(t, true, fields["email_verified"])
		assert.Equal(t, string(enums.RoleAdmin), fields["role"])
		assert.Equal(t, string(enums.StatusActive), fields["status"])
		assert.Equal(t, false, fields["banned"])
		assert.Equal(t, "Test reason", fields["ban_reason"])
	})

	t.Run("角色为null时拒绝", func(t *testing.T) {
		patch := user.AdminFieldSet{
			Role: omitnull.FromPtr[enums.Role](nil),
		}
		_, err := user.CollectAdminFields(ctx, nil, "usr_123", patch)
		require.Error(t, err)
	})
}
