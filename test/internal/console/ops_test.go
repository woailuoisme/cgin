package console_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/console"
)

func TestAppInitCommand(t *testing.T) {
	ctx := context.Background()

	t.Run("creation from example and populate JWT_SECRET", func(t *testing.T) {
		tempDir := t.TempDir()
		envPath := filepath.Join(tempDir, ".env")
		examplePath := filepath.Join(tempDir, ".env.example")

		exampleContent := "APP_ENV=local\nJWT_SECRET=\nFILESYSTEM_DISK=local\n"
		require.NoError(t, os.WriteFile(examplePath, []byte(exampleContent), 0o600))

		out, err := execWithTestFactory(ctx, nil,
			"app:init",
			"--env", envPath,
			"--env-example", examplePath,
			"--skip-db", "--skip-migrate", "--skip-seed", "--skip-s3",
		)
		require.NoError(t, err)
		assert.Contains(t, out, "Initializing cgin application environment")

		contentBytes, err := os.ReadFile(envPath)
		require.NoError(t, err)
		content := string(contentBytes)
		assert.Contains(t, content, "APP_ENV=local")
		assert.Contains(t, content, "FILESYSTEM_DISK=local")

		var jwtVal string
		for line := range strings.SplitSeq(content, "\n") {
			if val, ok := strings.CutPrefix(line, "JWT_SECRET="); ok {
				jwtVal = val
			}
		}
		assert.GreaterOrEqual(t, len(jwtVal), 32)
	})

	t.Run("preserve existing secrets without --force", func(t *testing.T) {
		tempDir := t.TempDir()
		envPath := filepath.Join(tempDir, ".env")
		existing := "APP_ENV=local\nJWT_SECRET=my_existing_secret_1234567890123456\n"
		require.NoError(t, os.WriteFile(envPath, []byte(existing), 0o600))

		_, err := execWithTestFactory(ctx, nil,
			"app:init",
			"--env", envPath,
			"--skip-db", "--skip-migrate", "--skip-seed", "--skip-s3",
		)
		require.NoError(t, err)

		contentBytes, err := os.ReadFile(envPath)
		require.NoError(t, err)
		assert.Contains(t, string(contentBytes), "JWT_SECRET=my_existing_secret_1234567890123456")
	})

	t.Run("force overwrite existing secrets", func(t *testing.T) {
		tempDir := t.TempDir()
		envPath := filepath.Join(tempDir, ".env")
		existing := "APP_ENV=local\nJWT_SECRET=old_jwt_123456789012345678901234567890\n"
		require.NoError(t, os.WriteFile(envPath, []byte(existing), 0o600))

		_, err := execWithTestFactory(ctx, nil,
			"app:init",
			"--env", envPath,
			"--force",
			"--skip-db", "--skip-migrate", "--skip-seed", "--skip-s3",
		)
		require.NoError(t, err)

		contentBytes, err := os.ReadFile(envPath)
		require.NoError(t, err)
		assert.NotContains(t, string(contentBytes), "old_jwt_123456789012345678901234567890")
	})
}

func TestGenerateRandomSecret(t *testing.T) {
	secret, err := console.GenerateRandomSecret(32)
	require.NoError(t, err)

	decoded, err := base64.StdEncoding.DecodeString(secret)
	require.NoError(t, err)
	assert.Len(t, decoded, 32)
	assert.GreaterOrEqual(t, len(secret), 32)
}

func TestJWTSecretCommand(t *testing.T) {
	ctx := context.Background()

	t.Run("show secret writes to IOStreams without touching filesystem", func(t *testing.T) {
		out, err := execWithTestFactory(ctx, nil, "jwt:secret", "--show")
		require.NoError(t, err)

		secret := strings.TrimSpace(out)
		assert.NotEmpty(t, secret)
		decoded, err := base64.StdEncoding.DecodeString(secret)
		require.NoError(t, err)
		assert.Len(t, decoded, 32)
	})

	t.Run("empty env path returns ValidationError", func(t *testing.T) {
		_, err := execWithTestFactory(ctx, nil, "jwt:secret", "--env", "")
		require.Error(t, err)
		var valErr *console.ValidationError
		require.ErrorAs(t, err, &valErr)
		assert.Equal(t, "env", valErr.Field)
	})

	t.Run("write env with --force", func(t *testing.T) {
		dir := t.TempDir()
		envPath := filepath.Join(dir, ".env")
		initialContent := "APP_NAME=cgin\nJWT_SECRET=old-secret-value\nAPP_ENV=local\n"
		require.NoError(t, os.WriteFile(envPath, []byte(initialContent), 0o600))

		_, err := execWithTestFactory(ctx, nil, "jwt:secret", "--env", envPath, "--force")
		require.NoError(t, err)

		updatedContent, err := os.ReadFile(envPath)
		require.NoError(t, err)

		var foundJWTSecret bool
		for line := range strings.SplitSeq(string(updatedContent), "\n") {
			if val, ok := strings.CutPrefix(line, "JWT_SECRET="); ok {
				foundJWTSecret = true
				assert.NotEqual(t, "old-secret-value", val)
				assert.GreaterOrEqual(t, len(val), 32)
			}
		}
		assert.True(t, foundJWTSecret)
	})

	t.Run("cancelled overwrite returns ErrCanceled", func(t *testing.T) {
		dir := t.TempDir()
		envPath := filepath.Join(dir, ".env")
		require.NoError(t, os.WriteFile(envPath, []byte("JWT_SECRET=existing-secret\n"), 0o600))

		mp := newMockPrompter()
		mp.ConfirmResponses = []bool{false}
		_, err := execWithTestFactory(ctx, &testFactory{PrompterValue: mp}, "jwt:secret", "--env", envPath)
		require.ErrorIs(t, err, console.ErrCanceled)
	})

	t.Run("deprecated space-separated invocation is rejected", func(t *testing.T) {
		_, err := execWithTestFactory(ctx, nil, "jwt", "secret", "--show")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown command "jwt"`)
	})
}

func makeMockXDBData() []byte {
	data := make([]byte, 512*1024+1024)
	binary.LittleEndian.PutUint16(data[0:2], 3)
	binary.LittleEndian.PutUint16(data[2:4], 1)
	binary.LittleEndian.PutUint32(data[4:8], 1700000000)
	binary.LittleEndian.PutUint32(data[8:12], 1024)
	binary.LittleEndian.PutUint32(data[12:16], 2048)
	binary.LittleEndian.PutUint16(data[16:18], 4)
	binary.LittleEndian.PutUint16(data[18:20], 4)
	return data
}

func TestGeoIPUpdateCommand(t *testing.T) {
	ctx := context.Background()

	t.Run("cancel when file exists and user declines", func(t *testing.T) {
		dir := t.TempDir()
		targetPath := filepath.Join(dir, "ip2region_v4.xdb")
		require.NoError(t, os.WriteFile(targetPath, []byte("existing"), 0o600))

		mp := newMockPrompter()
		mp.ConfirmResponses = []bool{false}
		out, err := execWithTestFactory(ctx, &testFactory{PrompterValue: mp}, "geoip:update", "--path", targetPath)
		require.ErrorIs(t, err, console.ErrCanceled)
		assert.Contains(t, out, "cancelled")
	})

	t.Run("success with mock server", func(t *testing.T) {
		mockData := makeMockXDBData()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "cgin-cli/geoip-updater", r.Header.Get("User-Agent"))
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(mockData)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mockData)
		}))
		defer server.Close()

		dir := t.TempDir()
		targetPath := filepath.Join(dir, "sub", "ip2region_v4.xdb")

		out, err := execWithTestFactory(ctx, nil, "geoip:update", "--path", targetPath, "--url", server.URL, "--force")
		require.NoError(t, err)

		content, err := os.ReadFile(targetPath)
		require.NoError(t, err)
		assert.Len(t, content, len(mockData))
		assert.Contains(t, out, "updated successfully")

		header, err := xdb.LoadHeaderFromBuff(content[:xdb.HeaderInfoLength])
		require.NoError(t, err)
		ver, err := xdb.VersionFromHeader(header)
		require.NoError(t, err)
		assert.NotNil(t, ver)
	})

	t.Run("server error and retry", func(t *testing.T) {
		mockData := makeMockXDBData()
		var requestCount atomic.Int32

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if requestCount.Add(1) <= 4 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(mockData)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mockData)
		}))
		defer server.Close()

		dir := t.TempDir()
		targetPath := filepath.Join(dir, "ip2region_v4.xdb")

		_, err := execWithTestFactory(ctx, nil, "geoip:update", "--path", targetPath, "--url", server.URL, "--force")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "502")

		_, err = execWithTestFactory(ctx, nil, "geoip:update", "--path", targetPath, "--url", server.URL, "--force")
		require.NoError(t, err)
		assert.FileExists(t, targetPath)
	})

	t.Run("verification failure cleans up temp file", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html>404 Not Found</html>"))
		}))
		defer server.Close()

		dir := t.TempDir()
		targetPath := filepath.Join(dir, "ip2region_v4.xdb")

		_, err := execWithTestFactory(ctx, nil, "geoip:update", "--path", targetPath, "--url", server.URL, "--force")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "verification failed")
		assert.NoFileExists(t, targetPath)

		entries, readErr := os.ReadDir(dir)
		require.NoError(t, readErr)
		for _, entry := range entries {
			assert.False(t, strings.HasPrefix(entry.Name(), "."), "临时文件未被清理: %s", entry.Name())
		}
	})
}
