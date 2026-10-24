package platform_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"bgin/internal/features/system"
	"bgin/internal/platform/temporal"
)

func TestSystemMaintenanceWorkflow(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	var acts *system.SystemActivities
	env.OnActivity(acts.CleanTempDirectory, mock.Anything, system.SystemMaintenanceInput{
		Directory:     "/tmp/uploads",
		RetentionDays: 7,
	}).Return(system.CleanResult{
		CleanedFiles: 10,
		FreedBytes:   1024 * 1024 * 50,
	}, nil)

	env.OnActivity(acts.ReportMaintenanceSummary, mock.Anything, system.CleanResult{
		CleanedFiles: 10,
		FreedBytes:   1024 * 1024 * 50,
	}).Return("success", nil)

	env.ExecuteWorkflow(system.SystemMaintenanceWorkflow, system.SystemMaintenanceInput{
		Directory:     "/tmp/uploads",
		RetentionDays: 7,
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result system.SystemMaintenanceOutput
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, 10, result.CleanedFiles)
	require.Equal(t, int64(1024*1024*50), result.FreedBytes)
	require.Equal(t, "success", result.Status)
}

func TestTemporalPinger(t *testing.T) {
	// 验证未启用状态下探针直接通过且报告 disabled
	pingerDisabled := temporal.NewPinger(nil, false)
	require.False(t, pingerDisabled.Enabled())
	require.Equal(t, "temporal", pingerDisabled.Driver())
	require.NoError(t, pingerDisabled.Ping(t.Context()))

	// 验证启用但 client 为 nil 时探测失败
	pingerEnabledNil := temporal.NewPinger(nil, true)
	require.True(t, pingerEnabledNil.Enabled())
	require.Equal(t, "temporal", pingerEnabledNil.Driver())
	require.Error(t, pingerEnabledNil.Ping(t.Context()))
}

func TestCleanTempDirectoryActivity(t *testing.T) {
	t.Parallel()

	acts := system.NewSystemActivities(nil)

	t.Run("nonexistent directory returns zero without error", func(t *testing.T) {
		res, err := acts.CleanTempDirectory(t.Context(), system.SystemMaintenanceInput{
			Directory:     "/path/to/nonexistent/temp/dir",
			RetentionDays: 7,
		})
		require.NoError(t, err)
		require.Equal(t, 0, res.CleanedFiles)
		require.Equal(t, int64(0), res.FreedBytes)
	})

	t.Run("refuses root or working directory", func(t *testing.T) {
		for _, dir := range []string{"/", ".", "\\"} {
			_, err := acts.CleanTempDirectory(t.Context(), system.SystemMaintenanceInput{
				Directory:     dir,
				RetentionDays: 7,
			})
			require.Error(t, err)
		}
	})

	t.Run("deletes expired files and retains fresh files", func(t *testing.T) {
		tempDir := t.TempDir()

		// 1. 创建过期文件
		oldFilePath := filepath.Join(tempDir, "expired.tmp")
		err := os.WriteFile(oldFilePath, []byte("old content"), 0o600)
		require.NoError(t, err)
		oldTime := time.Now().Add(-10 * 24 * time.Hour)
		err = os.Chtimes(oldFilePath, oldTime, oldTime)
		require.NoError(t, err)

		// 2. 创建新鲜文件
		freshFilePath := filepath.Join(tempDir, "fresh.tmp")
		err = os.WriteFile(freshFilePath, []byte("fresh content"), 0o600)
		require.NoError(t, err)

		// 3. 执行清理（保留 5 天）
		res, err := acts.CleanTempDirectory(t.Context(), system.SystemMaintenanceInput{
			Directory:     tempDir,
			RetentionDays: 5,
		})
		require.NoError(t, err)
		require.Equal(t, 1, res.CleanedFiles)
		require.Equal(t, int64(len("old content")), res.FreedBytes)

		// 验证过期文件被删除，新鲜文件依然存在
		_, err = os.Stat(oldFilePath)
		require.True(t, os.IsNotExist(err))

		_, err = os.Stat(freshFilePath)
		require.NoError(t, err)
	})

	t.Run("report summary activity", func(t *testing.T) {
		status, err := acts.ReportMaintenanceSummary(t.Context(), system.CleanResult{
			CleanedFiles: 1,
			FreedBytes:   100,
		})
		require.NoError(t, err)
		require.Equal(t, "success", status)
	})
}
