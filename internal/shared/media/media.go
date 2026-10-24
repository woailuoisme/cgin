// Package media 提供媒体文件上传、验证、处理、命名策略、Imgproxy 防篡改签名与多态实体附件管理。
package media

import (
	"github.com/uptrace/bun"

	"bgin/internal/config"
	"bgin/internal/platform/storage"
)

// New 根据配置与基础设施装配顶层多态媒体管理门面 Manager。
func New(
	mediaCfg config.MediaConfig,
	storageCfg config.StorageConfig,
	disk storage.Disk,
	db *bun.DB,
) *Manager {
	imgClient := NewWithStorage(mediaCfg.Imgproxy, storageCfg)

	pipelineOpts := []PipelineOption{
		WithImageProcessor(imgClient, mediaCfg.Imgproxy.DefaultFormat),
	}
	if mediaCfg.MaxFileSize > 0 {
		v := NewValidator().WithMaxFileSize(mediaCfg.MaxFileSize)
		if len(mediaCfg.AllowedExtensions) > 0 {
			v.WithAllowedExtensions(mediaCfg.AllowedExtensions...)
		}
		if len(mediaCfg.AllowedMimeTypes) > 0 {
			v.WithAllowedMimeTypes(mediaCfg.AllowedMimeTypes...)
		}
		pipelineOpts = append(pipelineOpts, WithValidator(v))
	}

	pipeline := NewPipeline(disk, pipelineOpts...)
	return NewManager(mediaCfg, disk, db, pipeline, imgClient)
}
