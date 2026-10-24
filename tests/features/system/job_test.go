package system_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/features/system"
	"bgin/internal/platform/queue"
)

func TestCleanupTempFilesJobContract(t *testing.T) {
	t.Parallel()

	job := system.NewCleanupTempFilesJob("temp")

	assert.Equal(t, system.TaskCleanupTempFiles, job.TaskName())
	assert.Equal(t, queue.DefaultQueueName, job.QueueName())
	assert.Equal(t, 2, job.MaxRetries())

	// 执行 Handle 测试
	err := job.Handle(t.Context())
	require.NoError(t, err)

	// 注册工厂测试
	reg := queue.DefaultRegistry()
	system.RegisterJobs(reg, nil)
}
