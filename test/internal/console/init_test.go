package console_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/console"
	"cgin/internal/console/core"
)

func TestAppInitCommand_CreationFromExample(t *testing.T) {
	tempDir := t.TempDir()
	envPath := filepath.Join(tempDir, ".env")
	examplePath := filepath.Join(tempDir, ".env.example")

	exampleContent := `APP_ENV=local
JWT_SECRET=
FILESYSTEM_DISK=local
`
	require.NoError(t, os.WriteFile(examplePath, []byte(exampleContent), 0o600))

	buf := new(bytes.Buffer)
	streams := &core.IOStreams{
		In:     os.Stdin,
		Out:    buf,
		ErrOut: buf,
	}
	factory := &console.TestFactory{StreamsValue: streams}

	opts := console.NewAppInitOptions(factory)
	opts.EnvPath = envPath
	opts.EnvExamplePath = examplePath
	opts.SkipDB = true
	opts.SkipMigrate = true
	opts.SkipSeed = true
	opts.SkipS3 = true

	err := opts.Run(context.Background())
	require.NoError(t, err)

	contentBytes, err := os.ReadFile(envPath)
	require.NoError(t, err)
	content := string(contentBytes)

	assert.Contains(t, content, "APP_ENV=local")
	assert.Contains(t, content, "FILESYSTEM_DISK=local")

	// 验证 JWT_SECRET 已被填充且长度满足安全要求
	lines := strings.Split(content, "\n")
	var jwtVal string
	for _, line := range lines {
		if val, ok := strings.CutPrefix(line, "JWT_SECRET="); ok {
			jwtVal = val
		}
	}

	assert.NotEmpty(t, jwtVal, "JWT_SECRET 必须非空")
	assert.GreaterOrEqual(t, len(jwtVal), 32, "JWT_SECRET 必须至少 32 字符")
}

func TestAppInitCommand_PreserveExistingSecrets(t *testing.T) {
	tempDir := t.TempDir()
	envPath := filepath.Join(tempDir, ".env")

	existingContent := `APP_ENV=local
JWT_SECRET=my_existing_secret_1234567890123456
`
	require.NoError(t, os.WriteFile(envPath, []byte(existingContent), 0o600))

	buf := new(bytes.Buffer)
	streams := &core.IOStreams{
		In:     os.Stdin,
		Out:    buf,
		ErrOut: buf,
	}
	factory := &console.TestFactory{StreamsValue: streams}

	opts := console.NewAppInitOptions(factory)
	opts.EnvPath = envPath
	opts.SkipDB = true
	opts.SkipMigrate = true
	opts.SkipSeed = true
	opts.SkipS3 = true

	err := opts.Run(context.Background())
	require.NoError(t, err)

	contentBytes, err := os.ReadFile(envPath)
	require.NoError(t, err)
	content := string(contentBytes)

	assert.Contains(t, content, "JWT_SECRET=my_existing_secret_1234567890123456")
}

func TestAppInitCommand_ForceOverwriteSecrets(t *testing.T) {
	tempDir := t.TempDir()
	envPath := filepath.Join(tempDir, ".env")

	existingContent := `APP_ENV=local
JWT_SECRET=old_jwt_123456789012345678901234567890
`
	require.NoError(t, os.WriteFile(envPath, []byte(existingContent), 0o600))

	buf := new(bytes.Buffer)
	streams := &core.IOStreams{
		In:     os.Stdin,
		Out:    buf,
		ErrOut: buf,
	}
	factory := &console.TestFactory{StreamsValue: streams}

	opts := console.NewAppInitOptions(factory)
	opts.EnvPath = envPath
	opts.Force = true
	opts.SkipDB = true
	opts.SkipMigrate = true
	opts.SkipSeed = true
	opts.SkipS3 = true

	err := opts.Run(context.Background())
	require.NoError(t, err)

	contentBytes, err := os.ReadFile(envPath)
	require.NoError(t, err)
	content := string(contentBytes)

	assert.NotContains(t, content, "old_jwt_123456789012345678901234567890")
}

func TestAppInitCommand_CLIInvocation(t *testing.T) {
	tempDir := t.TempDir()
	envPath := filepath.Join(tempDir, ".env")
	examplePath := filepath.Join(tempDir, ".env.example")
	require.NoError(t, os.WriteFile(examplePath, []byte("APP_ENV=local\nJWT_SECRET=\n"), 0o600))

	buf := new(bytes.Buffer)
	streams := &core.IOStreams{
		In:     os.Stdin,
		Out:    buf,
		ErrOut: buf,
	}
	factory := &console.TestFactory{StreamsValue: streams}

	cmd := console.NewRootCommandWithFactory(factory)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"app:init",
		"--env", envPath,
		"--env-example", examplePath,
		"--skip-db",
		"--skip-migrate",
		"--skip-seed",
		"--skip-s3",
	})

	err := cmd.ExecuteContext(context.Background())
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Initializing cgin application environment")
}
