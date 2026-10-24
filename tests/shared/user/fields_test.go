package user_test

import (
	"testing"
	"time"

	"github.com/aarondl/opt/omitnull"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/shared/enums"
	"cgin/internal/shared/user"
)

// TestPatchFromAdminFieldSet 验证封禁布尔状态与账号状态的协同一致性。
func TestPatchFromAdminFieldSet(t *testing.T) {
	t.Run("状态为banned时自动设置banned为true", func(t *testing.T) {
		patch, err := user.PatchFromAdminFieldSet(user.AdminFieldSet{
			Status: omitnull.From(enums.StatusBanned),
		})
		require.NoError(t, err)
		require.NotNil(t, patch.Banned)
		assert.True(t, *patch.Banned)
	})

	t.Run("banned为true时自动将状态改为banned", func(t *testing.T) {
		patch, err := user.PatchFromAdminFieldSet(user.AdminFieldSet{
			Banned: omitnull.From(true),
			Status: omitnull.From(enums.StatusActive),
		})
		require.NoError(t, err)
		require.NotNil(t, patch.Status)
		assert.Equal(t, enums.StatusBanned, *patch.Status)
	})

	t.Run("显式null的必填字符串返回校验错误", func(t *testing.T) {
		_, err := user.PatchFromAdminFieldSet(user.AdminFieldSet{
			Role: omitnull.FromPtr[enums.Role](nil),
		})
		require.Error(t, err)
	})
}

// TestPatchFromFieldSet 验证三态文本字段到“设置/清空”意图的映射。
func TestPatchFromFieldSet(t *testing.T) {
	t.Run("未传递字段保持不变", func(t *testing.T) {
		patch, err := user.PatchFromFieldSet(user.FieldSet{})
		require.NoError(t, err)
		assert.True(t, patch.Empty())
	})

	t.Run("显式null触发清空", func(t *testing.T) {
		patch, err := user.PatchFromFieldSet(user.FieldSet{
			Image:    omitnull.FromPtr[string](nil),
			Username: omitnull.FromPtr[string](nil),
		})
		require.NoError(t, err)
		assert.True(t, patch.ClearImage)
		assert.True(t, patch.ClearUsername)
		assert.True(t, patch.ClearDisplayName)
	})

	t.Run("纯空白视为清空", func(t *testing.T) {
		patch, err := user.PatchFromFieldSet(user.FieldSet{
			PhoneNumber: omitnull.From("   "),
		})
		require.NoError(t, err)
		assert.True(t, patch.ClearPhone)
	})

	t.Run("有效值写入参数并归一化时间", func(t *testing.T) {
		expires := time.Date(2026, 10, 1, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))
		patch, err := user.PatchFromAdminFieldSet(user.AdminFieldSet{
			BanExpires: omitnull.From(expires),
		})
		require.NoError(t, err)
		params := patch.Params("usr_1")
		require.NotNil(t, params.BanExpires)
		assert.Equal(t, time.UTC, params.BanExpires.Location())
	})
}
