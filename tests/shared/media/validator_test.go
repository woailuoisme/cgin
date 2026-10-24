// Package media_test 针对媒体处理平台模块的校验器安全规则进行完整契约测试。
package media_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/shared/media"
)

func TestSecurityValidator_Validate(t *testing.T) {
	ctx := t.Context()

	// 构造一个标准安全校验器：限制 1MB，禁止脚本与可执行文件
	validator := media.NewValidator().
		WithMaxFileSize(1024*1024).
		WithAllowedExtensions("png", "jpg", "jpeg", "webp").
		WithAllowedMimeTypes("image/png", "image/jpeg", "image/webp").
		WithDisallowedExtensions("php", "exe", "sh", "bat")

	t.Run("合法图片内容通过校验", func(t *testing.T) {
		err := validator.Validate(ctx, "avatar.png", 500*1024, "image/png")
		require.NoError(t, err)
	})

	t.Run("空文件大小拦截", func(t *testing.T) {
		err := validator.Validate(ctx, "empty.png", 0, "image/png")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty")
	})

	t.Run("超过大小上限拦截", func(t *testing.T) {
		err := validator.Validate(ctx, "large.png", 2*1024*1024, "image/png")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds maximum limit")
	})

	t.Run("高危黑名单后缀拦截", func(t *testing.T) {
		err := validator.Validate(ctx, "shell.php", 1024, "application/x-php")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "forbidden")
	})

	t.Run("不在允许扩展名列表拦截", func(t *testing.T) {
		err := validator.Validate(ctx, "document.pdf", 2048, "application/pdf")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not allowed")
	})

	t.Run("双后缀伪装攻击拦截", func(t *testing.T) {
		// 攻击者尝试用 shell.php.png 绕过前端检测
		err := validator.Validate(ctx, "shell.php.png", 2048, "image/png")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "forbidden dangerous extension")
	})

	t.Run("高危 MIME 类型拦截", func(t *testing.T) {
		err := validator.Validate(ctx, "script.png", 2048, "application/x-shellscript")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "forbidden")
	})
}
