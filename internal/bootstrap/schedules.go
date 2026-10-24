package bootstrap

import (
	"time"

	"bgin/internal/features/system"
	"bgin/internal/platform/queue"
)

// RegisterSchedules 集中编排并挂载全应用所有周期性定时任务规则（系统 Crontab 总表）。
//
// 遵循 Domain-First 规范：具体 Job 实现与业务逻辑由各 Feature 切片拥有，
// 本文件仅负责集中编排执行时间点（Cron 表达式）、目标队列与防重叠互斥约束。
func RegisterSchedules(sched *queue.Schedule) {
	if sched == nil {
		return
	}

	// 1. 系统维护与临时文件清理调度（每日凌晨 03:00 执行，加防重叠互斥锁）
	sched.Job(system.NewCleanupTempFilesJob("temp")).
		DailyAt("03:00").
		WithoutOverlapping(30 * time.Minute)
}
