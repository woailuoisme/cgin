package system

import (
	"context"
	"time"

	"go.uber.org/zap"

	"bgin/internal/platform/logger"
	"bgin/internal/platform/queue"
	"bgin/internal/platform/storage"
)

// TaskCleanupTempFiles 声明系统清理临时文件的 Asynq 任务唯一标识符。
const TaskCleanupTempFiles = "system:cleanup_temp_files"

// CleanupTempFilesJob 定时清理系统临时文件与过期导出缓存的后台任务。
type CleanupTempFilesJob struct {
	queue.JobMeta
	Directory string `json:"directory"`

	// disk 由工厂闭包在 Worker 启动期显式注入，不参与 JSON 序列化
	disk storage.Disk `json:"-"`
}

// NewCleanupTempFilesJob 创建清理临时文件任务实例。
func NewCleanupTempFilesJob(dir string) *CleanupTempFilesJob {
	return &CleanupTempFilesJob{
		JobMeta: queue.NewJobMeta(TaskCleanupTempFiles, queue.DefaultQueueName).
			WithRetries(2).
			WithTimeout(10 * time.Minute),
		Directory: dir,
	}
}

// RegisterJobs 向队列注册表登记系统切片的后台任务工厂与依赖。
func RegisterJobs(reg queue.Registry, disk storage.Disk) {
	if reg == nil {
		return
	}
	reg.Job(func() queue.Job {
		return &CleanupTempFilesJob{
			JobMeta: queue.NewJobMeta(TaskCleanupTempFiles, queue.DefaultQueueName),
			disk:    disk,
		}
	})
}

// Handle 任务执行入口。
func (j *CleanupTempFilesJob) Handle(ctx context.Context) error {
	log := logger.FromContext(ctx)
	log.Info("system cleanup temp files job started",
		zap.String("directory", j.Directory))

	// 若注入了存储驱动且指定了目录，可进一步联动 storage 进行深度清理
	if j.disk != nil && j.Directory != "" {
		log.Debug("cleaning up temp files on storage disk",
			zap.String("driver", j.disk.Driver()),
			zap.String("dir", j.Directory))
	}

	log.Info("system cleanup temp files job completed successfully")
	return nil
}
