package system

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"

	"bgin/internal/platform/logger"
	"bgin/internal/platform/storage"
)

// WorkflowNameSystemMaintenance 声明系统例行维护工作流的唯一标识。
const WorkflowNameSystemMaintenance = "system:maintenance_workflow"

// SystemMaintenanceInput 定义系统维护工作流的输入载荷。
type SystemMaintenanceInput struct {
	Directory     string `json:"directory"`
	RetentionDays int    `json:"retentionDays"`
}

// SystemMaintenanceOutput 定义系统维护工作流的最终汇总产物。
type SystemMaintenanceOutput struct {
	CleanedFiles int    `json:"cleanedFiles"`
	FreedBytes   int64  `json:"freedBytes"`
	Status       string `json:"status"`
}

// CleanResult 定义清理活动的中间阶段结果。
type CleanResult struct {
	CleanedFiles int   `json:"cleanedFiles"`
	FreedBytes   int64 `json:"freedBytes"`
}

// SystemMaintenanceWorkflow 是示例性的长时间多步骤系统维护工作流。
// 工作流函数本身保证绝对确定性（Deterministic），所有外部 I/O 均委托给 Activity 执行。
func SystemMaintenanceWorkflow(ctx workflow.Context, input SystemMaintenanceInput) (*SystemMaintenanceOutput, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    3,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var acts *SystemActivities

	var cleanRes CleanResult
	if err := workflow.ExecuteActivity(ctx, acts.CleanTempDirectory, input).Get(ctx, &cleanRes); err != nil {
		return nil, err
	}

	var reportStatus string
	if err := workflow.ExecuteActivity(ctx, acts.ReportMaintenanceSummary, cleanRes).Get(ctx, &reportStatus); err != nil {
		return nil, err
	}

	return &SystemMaintenanceOutput{
		CleanedFiles: cleanRes.CleanedFiles,
		FreedBytes:   cleanRes.FreedBytes,
		Status:       reportStatus,
	}, nil
}

// SystemActivities 封装系统切片的所有非确定性 I/O 操作与基础设施依赖。
type SystemActivities struct {
	disk storage.Disk
}

// NewSystemActivities 构造系统 Activity 集合并注入物理存储驱动。
func NewSystemActivities(disk storage.Disk) *SystemActivities {
	return &SystemActivities{disk: disk}
}

func isRootOrWorkingDir(dir string) bool {
	return dir == "." || dir == "/" || dir == "\\" || dir == filepath.VolumeName(dir)+string(filepath.Separator)
}

// validateCleanDirectory 校验并规范化待清理目录，拒绝根目录或当前工作目录。
func validateCleanDirectory(rawDir string) (string, error) {
	cleanDir := filepath.Clean(cmp.Or(strings.TrimSpace(rawDir), ".cache/excel"))
	if isRootOrWorkingDir(cleanDir) {
		return "", errors.New("system: refusing to clean root or working directory")
	}

	info, err := os.Stat(cleanDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("system: %s is not a directory", cleanDir)
	}

	return cleanDir, nil
}

// cleanExpiredFiles 使用安全 Root 作用域遍历并删除过期文件，防御 TOCTOU 与软链接逃逸。
func cleanExpiredFiles(ctx context.Context, cleanDir string, cutoff time.Time) (CleanResult, error) {
	root, err := os.OpenRoot(cleanDir)
	if err != nil {
		return CleanResult{}, err
	}
	defer func() { _ = root.Close() }()

	var res CleanResult
	err = fs.WalkDir(root.FS(), ".", func(relPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if relPath == "." || d.IsDir() {
			return nil
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}

		if info.ModTime().Before(cutoff) {
			if remErr := root.Remove(relPath); remErr == nil {
				res.CleanedFiles++
				res.FreedBytes += info.Size()
			}
		}
		return nil
	})
	return res, err
}

// CleanTempDirectory 执行清理临时文件的真实非确定性 I/O 操作。
func (a *SystemActivities) CleanTempDirectory(ctx context.Context, input SystemMaintenanceInput) (CleanResult, error) {
	log := logger.FromContext(ctx)
	log.Info("activity: cleaning temp directory",
		zap.String("dir", input.Directory),
		zap.Int("retention_days", input.RetentionDays))

	cleanDir, err := validateCleanDirectory(input.Directory)
	if err != nil {
		return CleanResult{}, err
	}
	if cleanDir == "" {
		log.Info("activity: directory does not exist, skipping cleanup")
		return CleanResult{}, nil
	}

	cutoff := time.Now()
	if input.RetentionDays > 0 {
		cutoff = cutoff.AddDate(0, 0, -input.RetentionDays)
	}

	res, err := cleanExpiredFiles(ctx, cleanDir, cutoff)
	if err != nil {
		return res, err
	}

	if a.disk != nil {
		log.Debug("activity: disk storage attached", zap.String("driver", a.disk.Driver()))
	}

	log.Info("activity: temp directory cleaned",
		zap.String("dir", cleanDir),
		zap.Int("cleaned_files", res.CleanedFiles),
		zap.Int64("freed_bytes", res.FreedBytes))

	return res, nil
}

// ReportMaintenanceSummary 记录维护产物或上报可观测性。
func (a *SystemActivities) ReportMaintenanceSummary(ctx context.Context, res CleanResult) (string, error) {
	log := logger.FromContext(ctx)
	log.Info("activity: maintenance completed",
		zap.Int("files", res.CleanedFiles),
		zap.Int64("freed_bytes", res.FreedBytes))

	return "success", nil
}

// RegisterTemporal 向 Temporal Worker 注册系统切片的工作流与活动。
func RegisterTemporal(w worker.Worker, disk storage.Disk) {
	if w == nil {
		return
	}
	w.RegisterWorkflow(SystemMaintenanceWorkflow)
	w.RegisterActivity(NewSystemActivities(disk))
}
