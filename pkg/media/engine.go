// Package media 提供媒体文件上传、验证、处理与 Imgproxy 防篡改签名服务。
package media

import (
	"context"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/samber/oops"

	"cgin/internal/config"
	"cgin/pkg/errs"
	"cgin/pkg/storage"
)

// DefaultPresignTTL 预签名上传链接默认有效期（15分钟）。
const DefaultPresignTTL = 15 * time.Minute

// PresignRequest 封装申请预签名上传的请求参数。
type PresignRequest struct {
	Category string `json:"category"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	MimeType string `json:"mime_type"`
}

// PresignResult 封装生成的预签名上传凭据。
type PresignResult struct {
	UploadURL string            `json:"upload_url"`
	Path      string            `json:"path"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresIn int64             `json:"expires_in"` // 有效秒数
}

// Preset 描述不可变的图像衍生处理规格值对象。
type Preset struct {
	Name         string
	Width        int
	Height       int
	ResizingType ResizingType
	Gravity      GravityType
	Quality      int
	Format       Format
}

// Options 将规格值对象解包为 imgproxy 处理函数选项序列。
func (p Preset) Options() []ProcessOption {
	var opts []ProcessOption
	if p.Width > 0 || p.Height > 0 || p.ResizingType != "" {
		opts = append(opts, WithResize(p.Width, p.Height))
		if p.ResizingType != "" {
			opts = append(opts, WithResizingType(p.ResizingType))
		}
	}
	if p.Gravity != "" {
		opts = append(opts, WithGravity(p.Gravity))
	}
	if p.Quality > 0 {
		opts = append(opts, WithQuality(p.Quality))
	}
	if p.Format != "" {
		opts = append(opts, WithFormat(p.Format))
	}
	return opts
}

// 标准领域图像规格预设值对象。
var (
	// PresetAvatarLarge 用户头像标准大图（256x256，焦点人脸居中裁剪，WebP）。
	PresetAvatarLarge = Preset{
		Name:         "avatar_large",
		Width:        256,
		Height:       256,
		ResizingType: ResizeFill,
		Gravity:      GravityFocusPoint,
		Format:       FormatWebP,
	}

	// PresetAvatarThumb 用户头像微缩图（64x64，焦点人脸居中裁剪，WebP）。
	PresetAvatarThumb = Preset{
		Name:         "avatar_thumb",
		Width:        64,
		Height:       64,
		ResizingType: ResizeFill,
		Gravity:      GravityFocusPoint,
		Format:       FormatWebP,
	}

	// PresetCover 封面横幅横图（1200x630，智能居中，WebP）。
	PresetCover = Preset{
		Name:         "cover",
		Width:        1200,
		Height:       630,
		ResizingType: ResizeFill,
		Gravity:      GravityCenter,
		Format:       FormatWebP,
	}
)

var defaultEngine Engine

// SetDefaultEngine 注册全局默认媒体引擎，供变体投影与防篡改签名安全使用。
func SetDefaultEngine(e Engine) {
	defaultEngine = e
}

// DefaultEngine 获取全局默认媒体引擎。
func DefaultEngine() Engine {
	return defaultEngine
}

// ImageVariants 封装一套完整的标准化图像衍生访问地址。
type ImageVariants struct {
	// 存储端相对路径（Key）
	Path string `json:"path" example:"uploads/avatars/20261031/uuid.png"`
	// 签名后标准尺寸访问 URL
	URL string `json:"url" example:"https://img.example.com/sig1/rs:fill:256:256:1/g:fp:0:0/...@webp"`
	// 签名后微缩图访问 URL
	ThumbURL string `json:"thumb_url" example:"https://img.example.com/sig2/rs:fill:64:64:1/g:fp:0:0/...@webp"`
}

// BuildImageVariants 根据相对路径与可选的媒体引擎，构建标准化图片衍生变体集。
// 未显式指定 engine 时自动使用全局 DefaultEngine()；路径为空时安全返回 nil。
func BuildImageVariants(rawPath string, engine ...Engine) *ImageVariants {
	cleanPath := strings.TrimSpace(rawPath)
	if cleanPath == "" {
		return nil
	}
	e := lo.FirstOr(engine, defaultEngine)
	if e == nil {
		return &ImageVariants{
			Path:     cleanPath,
			URL:      cleanPath,
			ThumbURL: cleanPath,
		}
	}
	return e.Variants(cleanPath)
}

// Engine 定义媒体中心统一深度服务契约。
type Engine interface {
	// Presign 校验文件参数并签发 S3 预签名直传凭证
	Presign(ctx context.Context, req PresignRequest) (*PresignResult, error)
	// Sign 生成指定规格的 Imgproxy 防篡改签名访问 URL
	Sign(path string, preset ...Preset) string
	// SignWithOptions 根据自定义函数式选项构建防篡改访问 URL
	SignWithOptions(path string, opts ...ProcessOption) (string, error)
	// Variants 为存储相对路径生成全套标准图片衍生变体集
	Variants(path string) *ImageVariants
	// Inspect 轻量解析图像基础元数据（宽高、格式、大小）
	Inspect(input []byte) (*ImageMeta, error)
	// Exists 校验对象在对象存储中是否存在
	Exists(ctx context.Context, path string) (bool, error)
	// Delete 删除对象存储中的指定路径
	Delete(ctx context.Context, path string) error
	// Ping 探针检测 Imgproxy 服务连通性（实现 ready.Pinger 契约）
	Ping(ctx context.Context) error
	// Driver 返回图像处理驱动标识
	Driver() string
}

// CategoryPolicy 描述业务存储类目的安全配额与格式契约。
type CategoryPolicy struct {
	MaxFileSize       int64
	AllowedExtensions []string
	AllowedMimeTypes  []string
}

// 默认内置的类目安全策略字典
var defaultCategoryPolicies = map[string]CategoryPolicy{
	"avatars": {
		MaxFileSize:       5 * 1024 * 1024, // 头像上限 5MB
		AllowedExtensions: []string{"png", "jpg", "jpeg", "webp", "gif"},
		AllowedMimeTypes:  []string{"image/png", "image/jpeg", "image/webp", "image/gif"},
	},
	"covers": {
		MaxFileSize:       10 * 1024 * 1024, // 封面横幅上限 10MB
		AllowedExtensions: []string{"png", "jpg", "jpeg", "webp"},
		AllowedMimeTypes:  []string{"image/png", "image/jpeg", "image/webp"},
	},
	"documents": {
		MaxFileSize:       50 * 1024 * 1024, // 文档上限 50MB
		AllowedExtensions: []string{"pdf", "xlsx", "xls", "docx", "doc", "csv", "zip"},
		AllowedMimeTypes:  []string{"application/pdf", "application/vnd.openxmlformats-officedocument.*", "application/zip", "text/csv"},
	},
}

// engineImpl 实现 Engine 接口。
type engineImpl struct {
	disk      storage.Disk
	image     *imgproxyClient
	validator *FileValidator
}

var _ Engine = (*engineImpl)(nil)

// NewEngine 构造带有工业级默认装配的统一媒体引擎。
func NewEngine(
	mediaCfg config.MediaConfig,
	storageCfg config.StorageConfig,
	disk storage.Disk,
) Engine {
	imgClient := newImgproxyClient(mediaCfg.Imgproxy, storageCfg)

	v := NewValidator()
	if mediaCfg.MaxFileSize > 0 {
		v.WithMaxFileSize(mediaCfg.MaxFileSize)
	}
	if len(mediaCfg.AllowedExtensions) > 0 {
		v.WithAllowedExtensions(mediaCfg.AllowedExtensions...)
	}
	if len(mediaCfg.AllowedMimeTypes) > 0 {
		v.WithAllowedMimeTypes(mediaCfg.AllowedMimeTypes...)
	}

	return &engineImpl{
		disk:      disk,
		image:     imgClient,
		validator: v,
	}
}

// Presign 校验文件参数并向底层对象存储申请预签名直传 URL。
func (e *engineImpl) Presign(ctx context.Context, req PresignRequest) (*PresignResult, error) {
	if e.disk == nil {
		return nil, oops.Code(errs.CodeInternal).In("media.Engine.Presign").Errorf("storage disk is not configured")
	}

	category := strings.ToLower(strings.TrimSpace(req.Category))
	if category == "" {
		category = "default"
	}

	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		return nil, oops.Code(errs.CodeBadRequest).In("media.Engine.Presign").Errorf("file name cannot be empty")
	}

	// 1. 全局危险扩展名/MIME与大小校验
	if err := e.validator.Validate(ctx, fileName, req.FileSize, req.MimeType); err != nil {
		return nil, oops.Code(errs.CodeBadRequest).In("media.Engine.Presign").Wrap(err)
	}

	// 2. 类目细粒度安全策略校验
	if err := validateCategoryPolicy(category, fileName, req.FileSize, req.MimeType); err != nil {
		return nil, oops.Code(errs.CodeBadRequest).In("media.Engine.Presign").Wrap(err)
	}

	// 3. 安全扩展名解析与单调 UUIDv7 目标路径推导
	ext := resolveExtension(fileName, req.MimeType)
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	uniqueFileName := id.String() + ext
	datePart := time.Now().UTC().Format("20060102")
	targetPath := path.Join("uploads", category, datePart, uniqueFileName)

	ttl := DefaultPresignTTL
	uploadURL, err := e.disk.PresignPut(ctx, targetPath, ttl)
	if err != nil {
		return nil, oops.Code(errs.CodeInternal).In("media.Engine.Presign").Wrap(err)
	}

	var headers map[string]string
	if mime := strings.TrimSpace(req.MimeType); mime != "" {
		headers = map[string]string{"Content-Type": mime}
	}

	return &PresignResult{
		UploadURL: uploadURL,
		Path:      targetPath,
		Method:    "PUT",
		Headers:   headers,
		ExpiresIn: int64(ttl.Seconds()),
	}, nil
}

// Sign 生成指定规格的 Imgproxy 防篡改签名访问 URL。
func (e *engineImpl) Sign(path string, preset ...Preset) string {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" || e.image == nil {
		return cleanPath
	}

	p := lo.FirstOr(preset, PresetAvatarLarge)
	u, err := e.image.URL(cleanPath, p.Options()...)
	if err != nil {
		return cleanPath
	}
	return u
}

// SignWithOptions 根据自定义函数式选项构建防篡改访问 URL。
func (e *engineImpl) SignWithOptions(path string, opts ...ProcessOption) (string, error) {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return "", errs.BadRequest("path cannot be empty")
	}
	if e.image == nil {
		return cleanPath, nil
	}
	return e.image.URL(cleanPath, opts...)
}

// Variants 为存储相对路径生成全套标准图片衍生变体集。
func (e *engineImpl) Variants(path string) *ImageVariants {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return nil
	}
	return &ImageVariants{
		Path:     cleanPath,
		URL:      e.Sign(cleanPath, PresetAvatarLarge),
		ThumbURL: e.Sign(cleanPath, PresetAvatarThumb),
	}
}

// Inspect 轻量解析图像基础元数据（宽高、格式、大小）。
func (e *engineImpl) Inspect(input []byte) (*ImageMeta, error) {
	if e.image == nil {
		return nil, oops.Code(errs.CodeInternal).In("media.Engine.Inspect").Errorf("image driver is not configured")
	}
	return e.image.Inspect(input)
}

// Exists 校验对象在对象存储中是否存在。
func (e *engineImpl) Exists(ctx context.Context, path string) (bool, error) {
	if e.disk == nil {
		return false, nil
	}
	return e.disk.Exists(ctx, path)
}

// Delete 删除对象存储中的指定路径。
func (e *engineImpl) Delete(ctx context.Context, path string) error {
	if e.disk == nil {
		return nil
	}
	return e.disk.Delete(ctx, path)
}

// Ping 执行底层 imgproxy 连通性探测。
func (e *engineImpl) Ping(ctx context.Context) error {
	if e.image == nil {
		return nil
	}
	return e.image.Ping(ctx)
}

// Driver 返回驱动标识。
func (e *engineImpl) Driver() string {
	if e.image != nil {
		return e.image.Driver()
	}
	return "imgproxy"
}

// sanitizeExtension 规范化并清洗文件扩展名（转小写、剔除控制字符、Null Byte 与路径穿越符号）。
func sanitizeExtension(name string) string {
	ext := strings.ToLower(filepath.Ext(strings.ReplaceAll(name, "\x00", "")))
	if len(ext) <= 1 || ext[0] != '.' {
		return ""
	}

	var b strings.Builder
	b.WriteByte('.')
	for _, r := range ext[1:] {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 1 {
		return ""
	}
	return b.String()
}

// resolveExtension 规范化并清洗文件扩展名，当原始扩展名缺失时基于 MIME 类型智能补全。
func resolveExtension(fileName, mimeType string) string {
	ext := sanitizeExtension(fileName)
	if ext != "" {
		return ext
	}
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/avif":
		return ".avif"
	case "application/pdf":
		return ".pdf"
	default:
		return ""
	}
}

// validateCategoryPolicy 针对存储类目执行专属配额与格式策略校验。
func validateCategoryPolicy(category, fileName string, fileSize int64, mimeType string) error {
	policy, ok := defaultCategoryPolicies[category]
	if !ok {
		return nil
	}

	if policy.MaxFileSize > 0 && fileSize > policy.MaxFileSize {
		return oops.Code(errs.CodeBadRequest).In("media.validateCategoryPolicy").
			Errorf("file size exceeds maximum limit of %d bytes for category %q", policy.MaxFileSize, category)
	}

	ext := strings.TrimPrefix(sanitizeExtension(fileName), ".")
	if len(policy.AllowedExtensions) > 0 && !slices.Contains(policy.AllowedExtensions, ext) {
		return oops.Code(errs.CodeBadRequest).In("media.validateCategoryPolicy").
			Errorf("file extension %q is not allowed for category %q", ext, category)
	}

	if len(policy.AllowedMimeTypes) > 0 && strings.TrimSpace(mimeType) != "" {
		cleanMime := strings.ToLower(strings.TrimSpace(mimeType))
		matched := slices.ContainsFunc(policy.AllowedMimeTypes, func(allowed string) bool {
			return cleanMime == allowed || strings.HasPrefix(cleanMime, strings.TrimSuffix(allowed, "*"))
		})
		if !matched {
			return oops.Code(errs.CodeBadRequest).In("media.validateCategoryPolicy").
				Errorf("file MIME type %q is not allowed for category %q", cleanMime, category)
		}
	}

	return nil
}
