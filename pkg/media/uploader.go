package media

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/samber/oops"

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

// Uploader 定义预签名文件直传凭据签发契约。
type Uploader interface {
	Presign(ctx context.Context, req PresignRequest) (*PresignResult, error)
}

// UploaderOption 定义 Uploader 构建选项函数。
type UploaderOption func(*uploaderImpl)

// WithValidator 注入自定义文件安全校验器。
func WithValidator(v Validator) UploaderOption {
	return func(u *uploaderImpl) {
		if v != nil {
			u.validator = v
		}
	}
}

// WithNamingStrategy 注入自定义路径命名策略。
func WithNamingStrategy(ns NamingStrategy) UploaderOption {
	return func(u *uploaderImpl) {
		if ns != nil {
			u.naming = ns
		}
	}
}

// WithCategoryPolicy 注册或覆盖指定类目的安全配额策略。
func WithCategoryPolicy(category string, policy CategoryPolicy) UploaderOption {
	return func(u *uploaderImpl) {
		clean := strings.ToLower(strings.TrimSpace(category))
		if clean != "" {
			u.policies[clean] = policy
		}
	}
}

// WithDefaultTTL 设置默认预签名有效期。
func WithDefaultTTL(ttl time.Duration) UploaderOption {
	return func(u *uploaderImpl) {
		if ttl > 0 {
			u.defaultTTL = ttl
		}
	}
}

type uploaderImpl struct {
	presigner  storage.Presigner
	validator  Validator
	naming     NamingStrategy
	policies   map[string]CategoryPolicy
	defaultTTL time.Duration
}

var _ Uploader = (*uploaderImpl)(nil)

// NewUploader 构造具有工业级默认安全策略的 Uploader 实例。
func NewUploader(presigner storage.Presigner, opts ...UploaderOption) Uploader {
	u := &uploaderImpl{
		presigner:  presigner,
		validator:  NewValidator(),
		naming:     NewUUIDv7NamingStrategy(),
		policies:   DefaultCategoryPolicies(),
		defaultTTL: DefaultPresignTTL,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(u)
		}
	}

	return u
}

// Presign 校验文件参数并向底层对象存储申请预签名直传凭证。
func (u *uploaderImpl) Presign(ctx context.Context, req PresignRequest) (*PresignResult, error) {
	if u.presigner == nil {
		return nil, oops.Code(errs.CodeInternal).In("media.Uploader.Presign").Wrap(storage.ErrPresignUnsupported)
	}

	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		return nil, oops.Code(errs.CodeBadRequest).In("media.Uploader.Presign").Errorf("file name cannot be empty")
	}

	category := strings.ToLower(strings.TrimSpace(req.Category))
	if category == "" {
		category = "default"
	}

	// 1. 全局危险扩展名/MIME与大小基础校验
	if err := u.validator.Validate(ctx, fileName, req.FileSize, req.MimeType); err != nil {
		return nil, oops.Code(errs.CodeBadRequest).In("media.Uploader.Presign").Wrap(err)
	}

	// 2. 类目专属细粒度安全配额校验
	if err := u.validateCategoryPolicy(category, fileName, req.FileSize, req.MimeType); err != nil {
		return nil, oops.Code(errs.CodeBadRequest).In("media.Uploader.Presign").Wrap(err)
	}

	// 3. 基于命名策略推导目标唯一存储路径
	targetPath := u.naming.Generate(category, fileName, req.MimeType)

	// 4. 向底层存储申请预签名 PUT URL
	ttl := cmp.Or(u.defaultTTL, DefaultPresignTTL)
	uploadURL, err := u.presigner.PresignPut(ctx, targetPath, ttl)
	if err != nil {
		return nil, oops.Code(errs.CodeInternal).In("media.Uploader.Presign").Wrap(err)
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

// validateCategoryPolicy 针对存储类目执行专属配额与格式策略校验。
func (u *uploaderImpl) validateCategoryPolicy(category, fileName string, fileSize int64, mimeType string) error {
	policy, ok := u.policies[category]
	if !ok {
		return nil
	}

	if policy.MaxFileSize > 0 && fileSize > policy.MaxFileSize {
		return oops.Code(errs.CodeBadRequest).In("media.validateCategoryPolicy").
			Errorf("file size exceeds maximum limit of %d bytes for category %q", policy.MaxFileSize, category)
	}

	ext := strings.TrimPrefix(SanitizeExtension(fileName), ".")
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
