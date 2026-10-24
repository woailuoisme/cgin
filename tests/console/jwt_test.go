package console_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/console"
)

// TestGenerateRandomSecret 验证随机密钥生成的长度与有效性。
func TestGenerateRandomSecret(t *testing.T) {
	secret, err := console.GenerateRandomSecret(32)
	require.NoError(t, err)

	decoded, err := base64.StdEncoding.DecodeString(secret)
	require.NoError(t, err)
	assert.Len(t, decoded, 32, "解码后必须为 32 字节")
	assert.GreaterOrEqual(t, len(secret), 32, "字符串形式长度必须满足 >= 32 位约束")
}

// TestJWTSecretCommand_Show 验证 --show 参数在不改动文件的情况下正常运行并由 OutOrStdout 捕获。
func TestJWTSecretCommand_Show(t *testing.T) {
	t.Run("colon command (jwt:secret)", func(t *testing.T) {
		cmd := console.NewRootCommand()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetArgs([]string{"jwt:secret", "--show"})
		err := cmd.ExecuteContext(context.Background())
		require.NoError(t, err)

		secret := strings.TrimSpace(buf.String())
		assert.NotEmpty(t, secret)
		decoded, err := base64.StdEncoding.DecodeString(secret)
		require.NoError(t, err)
		assert.Len(t, decoded, 32)
	})

	t.Run("deprecated space-separated invocation is rejected", func(t *testing.T) {
		cmd := console.NewRootCommand()
		cmd.SetArgs([]string{"jwt", "secret", "--show"})
		err := cmd.ExecuteContext(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown command "jwt"`)
	})
}

// TestJWTSecretCommand_WriteEnv 验证 --force 写入或更新 .env 中的 JWT_SECRET。
func TestJWTSecretCommand_WriteEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	initialContent := "APP_NAME=bgin\nJWT_SECRET=old-secret-value\nAPP_ENV=local\n"
	require.NoError(t, os.WriteFile(envPath, []byte(initialContent), 0o600))

	t.Chdir(dir)

	cmd := console.NewRootCommand()
	cmd.SetArgs([]string{"jwt:secret", "--force"})
	err := cmd.ExecuteContext(context.Background())
	require.NoError(t, err)

	updatedContent, err := os.ReadFile(envPath)
	require.NoError(t, err)

	lines := strings.Split(string(updatedContent), "\n")
	var foundJWTSecret bool
	for _, line := range lines {
		if strings.HasPrefix(line, "JWT_SECRET=") {
			foundJWTSecret = true
			val := strings.TrimPrefix(line, "JWT_SECRET=")
			assert.NotEqual(t, "old-secret-value", val, "旧密钥应已被替换")
			assert.GreaterOrEqual(t, len(val), 32, "新密钥长度应 >= 32")
		}
	}
	assert.True(t, foundJWTSecret, ".env 中必须包含 JWT_SECRET")
}
