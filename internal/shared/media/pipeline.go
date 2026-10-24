// Package media 提供媒体文件上传、验证、处理、命名策略与 Imgproxy 防篡改签名服务。
package media

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/samber/lo"
	"github.com/samber/oops"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/storage"
)

// defaultOctetStreamMime 默认通用二进制流 MIME 类型。
const defaultOctetStreamMime = "application/octet-stream"

// bufferPool 提供高并发流式上传场景下的字节缓冲区复用池，减轻 GC 压力。
var bufferPool = sync.Pool{
	New: func() any {
		return bytes.NewBuffer(make([]byte, 0, 64*1024))
	},
}

// getBuffer 从缓冲区池中获取并重置字节缓冲区。
func getBuffer() *bytes.Buffer {
	if buf, ok := bufferPool.Get().(*bytes.Buffer); ok {
		buf.Reset()
		return buf
	}
	return bytes.NewBuffer(make([]byte, 0, 64*1024))
}

// putBuffer 将小于容量上限（4MB）的缓冲区重置后归还至池中。
func putBuffer(buf *bytes.Buffer) {
	if buf != nil && buf.Cap() <= 4*1024*1024 {
		buf.Reset()
		bufferPool.Put(buf)
	}
}

// readMultipartFile 从 multipart.FileHeader 打开并读取文件内容至临时缓冲池对象。
// 调用方使用完毕后必须负责通过 putBuffer(buf) 归还缓冲池。
func readMultipartFile(file *multipart.FileHeader) (*bytes.Buffer, error) {
	src, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = src.Close() }()

	buf := getBuffer()
	if _, err := io.Copy(buf, src); err != nil {
		putBuffer(buf)
		return nil, err
	}
	return buf, nil
}

// FileInfo 封装文件上传成功后的完整元数据描述。
type FileInfo struct {
	Path         string    `json:"path"`
	URL          string    `json:"url"`
	FileName     string    `json:"fileName"`
	OriginalName string    `json:"originalName"`
	MimeType     string    `json:"mimeType"`
	Size         int64     `json:"size"`
	Hash         string    `json:"hash"`
	UploadedAt   time.Time `json:"uploadedAt"`
}

// NamingStrategy 定义根据文件内容与原始文件名生成安全目标文件名与内容哈希的策略契约。
type NamingStrategy interface {
	GenerateName(ctx context.Context, originalName string, content []byte) (fileName, hash string)
}

// PathStrategy 定义将文件分散存储到分区目录（如按业务类别与日期）的路径规划契约。
type PathStrategy interface {
	GeneratePath(ctx context.Context, category, fileName string) string
}

// Validator 定义文件安全性、大小上限及 MIME 格式兼容性的校验契约。
type Validator interface {
	Validate(ctx context.Context, originalName string, size int64, mimeType string) error
}

// SizeValidator 是可选能力：实现它的 Validator 允许调用方在读取文件内容之前，
// 先按 multipart 头声明的长度拒绝超大文件，避免整读进内存后才失败。
// 未实现该接口的校验器只是失去这层前置优化，完整校验流程不受影响。
type SizeValidator interface {
	ValidateSize(size int64) error
}

// ImageProcessor 统一定义图像处理与防篡改签名契约。
type ImageProcessor = Client

// Pipeline 定义媒体与文件处理管道核心门面契约。
type Pipeline interface {
	// Upload 校验并存储单个 multipart 表单上传文件。
	Upload(ctx context.Context, file *multipart.FileHeader, category string, diskName ...string) (*FileInfo, error)
	// UploadMultiple 批量校验并存储多个 multipart 表单上传文件。
	UploadMultiple(ctx context.Context, files []*multipart.FileHeader, category string, diskName ...string) ([]*FileInfo, error)
	// UploadBytes 存储原生字节切片。
	UploadBytes(ctx context.Context, content []byte, originalName, category string, diskName ...string) (*FileInfo, error)
	// UploadReader 流式存储指定数据。
	UploadReader(ctx context.Context, reader io.Reader, originalName string, size int64, mimeType, category string, diskName ...string) (*FileInfo, error)
	// UploadImage 处理图片上传，校验图像合法性并持久化。
	UploadImage(ctx context.Context, file *multipart.FileHeader, category string, diskName ...string) (*FileInfo, error)
	// Delete 删除指定路径的文件。
	Delete(ctx context.Context, path string, diskName ...string) error
	// URL 获取指定文件的公开访问 URL。
	URL(path string, diskName ...string) string
	// Image 返回图像处理与防篡改签名器。
	Image() ImageProcessor
}

// PipelineOption 定义用于定制 Pipeline 的函数式选项。
type PipelineOption func(*PipelineImpl)

// WithNamingStrategy 自定义文件命名生成策略。
func WithNamingStrategy(naming NamingStrategy) PipelineOption {
	return func(p *PipelineImpl) {
		if naming != nil {
			p.naming = naming
		}
	}
}

// WithHashNaming 快捷配置基于 SHA-256 内容哈希的命名策略。
func WithHashNaming(hashLength ...int) PipelineOption {
	return WithNamingStrategy(NewHashNamingStrategy(hashLength...))
}

// WithUUIDNaming 快捷配置基于单调递增 UUIDv7 的文件名生成策略。
func WithUUIDNaming() PipelineOption {
	return WithNamingStrategy(NewUUIDNamingStrategy())
}

// WithPathStrategy 自定义存储分区路径规划策略。
func WithPathStrategy(path PathStrategy) PipelineOption {
	return func(p *PipelineImpl) {
		if path != nil {
			p.path = path
		}
	}
}

// WithCategoryDatePath 快捷配置按业务类别与日期分区的路径策略（uploads/{category}/{YYYYMMDD}/{filename}）。
func WithCategoryDatePath(basePrefix ...string) PipelineOption {
	return WithPathStrategy(NewCategoryDatePathStrategy(basePrefix...))
}

// WithDatePath 快捷配置按日期分区的相对存储路径策略（uploads/{YYYYMMDD}/{filename}）。
func WithDatePath(basePrefix ...string) PipelineOption {
	return WithPathStrategy(NewDatePathStrategy(basePrefix...))
}

// WithValidator 自定义上传文件校验器。
func WithValidator(validator Validator) PipelineOption {
	return func(p *PipelineImpl) {
		if validator != nil {
			p.validator = validator
		}
	}
}

// WithImageProcessor 注入图像处理器。
func WithImageProcessor(proc ImageProcessor, targetFormat ...string) PipelineOption {
	return func(p *PipelineImpl) {
		p.imageProcessor = proc
		p.targetImageFormat = cmp.Or(lo.FirstOrEmpty(targetFormat), "webp")
	}
}

// PipelineImpl 实现了 Pipeline 接口，整合可插拔命名、路径分区、安全校验、底层存储与图像签名。
type PipelineImpl struct {
	disk              storage.Disk
	naming            NamingStrategy
	path              PathStrategy
	validator         Validator
	imageProcessor    ImageProcessor
	targetImageFormat string
}

// NewPipeline 构建并初始化带有工业级默认配置的媒体管道实例。
func NewPipeline(disk storage.Disk, opts ...PipelineOption) *PipelineImpl {
	p := &PipelineImpl{
		disk:      disk,
		naming:    NewHashNamingStrategy(16),
		path:      NewCategoryDatePathStrategy("uploads"),
		validator: NewValidator(),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// NewService 兼容工厂函数。
func NewService(disk storage.Disk, opts ...PipelineOption) *PipelineImpl {
	return NewPipeline(disk, opts...)
}

// Image 返回图像处理与防篡改签名器。
func (p *PipelineImpl) Image() ImageProcessor {
	return p.imageProcessor
}

// Upload 校验并存储单个 multipart 表单上传文件，利用缓冲区池降低内存分配。
func (p *PipelineImpl) Upload(ctx context.Context, file *multipart.FileHeader, category string, diskName ...string) (*FileInfo, error) {
	if file == nil {
		return nil, errs.BadRequest("uploaded file is nil")
	}

	// 前置尺寸拒绝：multipart 头已声明长度时先行拦截，
	// 避免超大文件被整读进内存后才在校验阶段失败。
	if sizeValidator, ok := p.validator.(SizeValidator); ok && file.Size > 0 {
		if err := sizeValidator.ValidateSize(file.Size); err != nil {
			return nil, oops.Code(errs.CodeBadRequest).In("media.Upload.PrecheckSize").Wrap(err)
		}
	}

	buf, err := readMultipartFile(file)
	if err != nil {
		return nil, oops.Code(errs.CodeBadRequest).In("media.Upload").Wrap(err)
	}
	defer putBuffer(buf)

	content := buf.Bytes()
	mimeType := detectMimeType(file.Filename, content)
	headerMime := file.Header.Get("Content-Type")
	if mimeType == defaultOctetStreamMime && headerMime != "" && headerMime != defaultOctetStreamMime {
		mimeType = headerMime
	}

	return p.uploadBytes(ctx, content, file.Filename, mimeType, category, diskName...)
}

// UploadMultiple 批量校验并存储多个 multipart 表单上传文件（任一失败自动回滚已成功上传的文件）。
func (p *PipelineImpl) UploadMultiple(ctx context.Context, files []*multipart.FileHeader, category string, diskName ...string) ([]*FileInfo, error) {
	if len(files) == 0 {
		return nil, errs.BadRequest("no files provided for upload")
	}

	results := make([]*FileInfo, 0, len(files))
	for _, file := range files {
		info, err := p.Upload(ctx, file, category, diskName...)
		if err != nil {
			for _, uploaded := range results {
				_ = p.Delete(ctx, uploaded.Path, diskName...)
			}
			return nil, err
		}
		results = append(results, info)
	}

	return results, nil
}

// UploadBytes 将原生字节切片进行安全校验与哈希命名后上传至存储驱动。
func (p *PipelineImpl) UploadBytes(ctx context.Context, content []byte, originalName, category string, diskName ...string) (*FileInfo, error) {
	if len(content) == 0 {
		return nil, errs.BadRequest("uploaded file content cannot be empty")
	}

	mimeType := detectMimeType(originalName, content)
	return p.uploadBytes(ctx, content, originalName, mimeType, category, diskName...)
}

// UploadReader 流式校验并写入任意 io.Reader 数据流至底层存储磁盘。
func (p *PipelineImpl) UploadReader(
	ctx context.Context,
	reader io.Reader,
	originalName string,
	size int64,
	mimeType string,
	category string,
	diskName ...string,
) (*FileInfo, error) {
	if reader == nil {
		return nil, errs.BadRequest("reader cannot be nil")
	}

	maxSize := size
	if fv, ok := p.validator.(*FileValidator); ok && fv != nil && fv.MaxFileSize > 0 {
		if maxSize <= 0 || maxSize > fv.MaxFileSize {
			maxSize = fv.MaxFileSize
		}
	}

	limitedReader := reader
	if maxSize > 0 {
		limitedReader = io.LimitReader(reader, maxSize+1)
	}

	buf := getBuffer()
	defer putBuffer(buf)

	if _, err := io.Copy(buf, limitedReader); err != nil {
		return nil, oops.Code(errs.CodeInternal).In("media.UploadReader").Wrap(err)
	}

	content := buf.Bytes()
	if maxSize > 0 && int64(len(content)) > maxSize {
		return nil, errs.BadRequest(fmt.Sprintf("uploaded content exceeds maximum allowed size of %d bytes", maxSize))
	}

	if strings.TrimSpace(mimeType) == "" {
		mimeType = detectMimeType(originalName, content)
	}

	return p.uploadBytes(ctx, content, originalName, mimeType, category, diskName...)
}

// UploadImage 处理图片上传，校验原始图像合法性并持久化保存原图。
func (p *PipelineImpl) UploadImage(ctx context.Context, file *multipart.FileHeader, category string, diskName ...string) (*FileInfo, error) {
	if file == nil {
		return nil, oops.Code(errs.CodeBadRequest).In("media.UploadImage").Errorf("file header cannot be nil")
	}

	buf, err := readMultipartFile(file)
	if err != nil {
		return nil, oops.Code(errs.CodeInternal).In("media.UploadImage.Open").Wrap(err)
	}
	defer putBuffer(buf)

	rawBytes := buf.Bytes()
	if p.imageProcessor != nil {
		if _, inspectErr := p.imageProcessor.Inspect(rawBytes); inspectErr != nil {
			return nil, oops.Code(errs.CodeBadRequest).In("media.UploadImage.Inspect").Wrap(inspectErr)
		}
	}

	return p.UploadBytes(ctx, rawBytes, file.Filename, category, diskName...)
}

// Delete 根据相对存储路径删除指定磁盘上的文件。
func (p *PipelineImpl) Delete(ctx context.Context, filePath string, diskName ...string) error {
	disk := p.getDisk(diskName...)
	if disk == nil {
		return errs.Internal(errors.New("storage disk unavailable"))
	}

	return disk.Delete(ctx, filePath)
}

// URL 获取指定文件的公开访问 URL。
func (p *PipelineImpl) URL(filePath string, diskName ...string) string {
	disk := p.getDisk(diskName...)
	if disk == nil {
		return ""
	}

	url, _ := disk.URL(filePath)
	return url
}

// uploadBytes 是上传字节切片的内部核心流程，包含安全校验、可选图片转码、策略命名、路径生成与磁盘写入。
func (p *PipelineImpl) uploadBytes(
	ctx context.Context,
	content []byte,
	originalName string,
	mimeType string,
	category string,
	diskName ...string,
) (*FileInfo, error) {
	size := int64(len(content))

	if p.validator != nil {
		if err := p.validator.Validate(ctx, originalName, size, mimeType); err != nil {
			return nil, oops.Code(errs.CodeBadRequest).In("media.Validate").Wrap(err)
		}
	}

	if p.imageProcessor != nil && strings.HasPrefix(mimeType, "image/") && mimeType != "image/svg+xml" {
		if _, inspectErr := p.imageProcessor.Inspect(content); inspectErr != nil {
			return nil, oops.Code(errs.CodeBadRequest).In("media.InspectImage").Wrap(inspectErr)
		}
	}

	fileName, hash := p.naming.GenerateName(ctx, originalName, content)
	storagePath := p.path.GeneratePath(ctx, category, fileName)

	disk := p.getDisk(diskName...)
	if disk == nil {
		return nil, errs.Internal(errors.New("storage disk unavailable"))
	}

	if err := disk.Put(ctx, storagePath, content); err != nil {
		return nil, oops.Code(errs.CodeInternal).In("media.Put").Wrap(err)
	}

	url, _ := disk.URL(storagePath)

	return &FileInfo{
		Path:         storagePath,
		URL:          url,
		FileName:     fileName,
		OriginalName: originalName,
		MimeType:     mimeType,
		Size:         size,
		Hash:         hash,
		UploadedAt:   time.Now().UTC(),
	}, nil
}

// getDisk 返回当前 Pipeline 装配的存储磁盘。
func (p *PipelineImpl) getDisk(_ ...string) storage.Disk {
	return p.disk
}

// detectMimeType 结合魔数头嗅探与扩展名推导文件的真实 MIME 类型。
func detectMimeType(filename string, content []byte) string {
	if len(content) > 0 {
		if detected := mimetype.Detect(content).String(); detected != "" && detected != defaultOctetStreamMime {
			return detected
		}
	}

	if ext := filepath.Ext(filename); ext != "" {
		if byExt := mime.TypeByExtension(ext); byExt != "" {
			return byExt
		}
	}

	return defaultOctetStreamMime
}
