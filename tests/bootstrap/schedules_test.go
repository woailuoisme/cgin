package bootstrap_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/bootstrap"
	"bgin/internal/platform/queue"
)

func TestRegisterSchedulesContract(t *testing.T) {
	t.Parallel()

	sched := queue.NewSchedule()
	bootstrap.RegisterSchedules(sched)

	entries := sched.Entries()
	require.NotEmpty(t, entries)

	// 查找并校验清理任务的调度配置
	var cleanupEntry *queue.ScheduleEntry
	for _, e := range entries {
		if e.Spec() == "0 3 * * *" {
			cleanupEntry = e
			break
		}
	}

	require.NotNil(t, cleanupEntry, "expected cleanup temp files schedule entry at 03:00")
	assert.True(t, cleanupEntry.IsValid())
	assert.True(t, cleanupEntry.IsWithoutOverlapping())
	assert.Equal(t, 30*time.Minute, cleanupEntry.LockTTL())

	// 编译挂载保障幂等性
	sched.Register()
	sched.Register()
}
