package storage

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/samber/lo"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/resilience"
)

// defaultS3Region 是 minio 的签名要求与 S3 兼容实现的默认值不一致时的兜底：
// 多数兼容实现（Garage、MinIO）不校验区域，AWS 则必须给出正确的桶区域。
const defaultS3Region = "us-east-1"

// 用于从已知长度的读取器上推断对象大小：minio 拿到 -1 会退回分片上传，
// 已知长度时走单次 PutObject 更省往返。
type (
	lenReader  interface{ Len() int }
	sizeReader interface{ Size() int64 }
)

// s3Disk 用 minio-go/v7 实现 Disk，对接任何 S3 兼容对象存储。
// 编译期断言：驱动一旦不再满足 Disk 契约，构建就失败在这里，而不是在 Open 的赋值处。
var _ Disk = (*s3Disk)(nil)

type s3Disk struct {
	client    *minio.Client
	bucket    string
	region    string
	endpoint  string
	useSSL    bool
	publicURL string
}

// NewS3Disk 构造 S3 兼容驱动。必填项缺失时直接失败——这些值由 config.validate 在更早的
// 阶段拦截，这里的检查是防止绕过配置层直接调用的兜底。
func NewS3Disk(cfg config.S3StorageConfig) (Disk, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("storage: AWS_ENDPOINT cannot be empty")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("storage: AWS_BUCKET cannot be empty")
	}
	if strings.TrimSpace(cfg.AccessKeyID) == "" || strings.TrimSpace(cfg.SecretAccessKey) == "" {
		return nil, errors.New("storage: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY cannot be empty")
	}

	region := cmp.Or(cfg.Region, defaultS3Region)

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure:    cfg.UseSSL,
		Region:    region,
		Transport: resilience.NewResilientTransport(nil, resilience.ResilientTransportConfig{}),
	})
	if err != nil {
		return nil, fmt.Errorf("storage: failed to initialize S3 client (endpoint=%s): %w", cfg.Endpoint, err)
	}

	return &s3Disk{
		client:    client,
		bucket:    cfg.Bucket,
		region:    region,
		endpoint:  cfg.Endpoint,
		useSSL:    cfg.UseSSL,
		publicURL: strings.TrimSuffix(cfg.PublicURL, "/"),
	}, nil
}

// SupportsPresign 显式声明 S3 兼容驱动原生具备预签名能力。
func (d *s3Disk) SupportsPresign() bool {
	return true
}

// isNotFound 判断底层错误是否为对象不存在（404 / NoSuchKey）。
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.Code == "NotFound" ||
		resp.StatusCode == http.StatusNotFound
}

func (d *s3Disk) wrapS3Error(op, key string, err error) error {
	if isNotFound(err) {
		return notFound(key)
	}
	return opFailed(op, key, err)
}

// statObject 取对象元数据，对象不存在时转成统一的 404。
func (d *s3Disk) statObject(ctx context.Context, key string) (minio.ObjectInfo, error) {
	info, err := d.client.StatObject(ctx, d.bucket, keyOf(key), minio.StatObjectOptions{})
	if err != nil {
		return minio.ObjectInfo{}, d.wrapS3Error("stat s3 object", key, err)
	}
	return info, nil
}

// openObject 打开对象流。GetObject 是惰性的，不存在要到 Stat 时才暴露，因此这里补一次 Stat。
func (d *s3Disk) openObject(ctx context.Context, key string) (*minio.Object, error) {
	obj, err := d.client.GetObject(ctx, d.bucket, keyOf(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, d.wrapS3Error("open s3 object stream", key, err)
	}

	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, d.wrapS3Error("stat s3 object", key, err)
	}
	return obj, nil
}

// Put 写入整个对象，内容类型由内容嗅探得出。
func (d *s3Disk) Put(ctx context.Context, key string, contents []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := d.client.PutObject(ctx, d.bucket, keyOf(key), bytes.NewReader(contents),
		int64(len(contents)), minio.PutObjectOptions{
			ContentType: http.DetectContentType(contents),
		})
	if err != nil {
		return opFailed("write s3 object", key, err)
	}
	return nil
}

// PutStream 从流写入对象；读取器能自报长度时按已知长度上传，否则退回分片上传。
func (d *s3Disk) PutStream(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	var size int64 = -1
	switch v := r.(type) {
	case lenReader:
		size = int64(v.Len())
	case sizeReader:
		size = v.Size()
	}

	if _, err := d.client.PutObject(ctx, d.bucket, keyOf(key), r, size, minio.PutObjectOptions{}); err != nil {
		return opFailed("write s3 object", key, err)
	}
	return nil
}

// PutMultipart 从 HTTP multipart 表单头写入对象。
func (d *s3Disk) PutMultipart(ctx context.Context, key string, file *multipart.FileHeader) error {
	if file == nil {
		return errs.BadRequest("Upload file cannot be nil")
	}
	src, err := file.Open()
	if err != nil {
		return opFailed("open upload file", key, err)
	}
	defer func() { _ = src.Close() }()

	contentType := cmp.Or(file.Header.Get("Content-Type"), "application/octet-stream")

	_, err = d.client.PutObject(ctx, d.bucket, keyOf(key), src, file.Size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return opFailed("write s3 object", key, err)
	}
	return nil
}

// Get 读出对象的全部字节。
func (d *s3Disk) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	obj, err := d.openObject(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = obj.Close() }()

	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, opFailed("read s3 object", key, err)
	}
	return data, nil
}

// GetStream 返回对象的只读流。
func (d *s3Disk) GetStream(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return d.openObject(ctx, key)
}

// Delete 删除对象；对象本就不存在时视为成功，使删除操作天然幂等。
func (d *s3Disk) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := d.client.RemoveObject(ctx, d.bucket, keyOf(key), minio.RemoveObjectOptions{}); err != nil && !isNotFound(err) {
		return opFailed("delete s3 object", key, err)
	}
	return nil
}

// Exists 报告对象是否存在。
func (d *s3Disk) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	if _, err := d.client.StatObject(ctx, d.bucket, keyOf(key), minio.StatObjectOptions{}); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, opFailed("check s3 object", key, err)
	}
	return true, nil
}

// Size 返回对象的字节数。
func (d *s3Disk) Size(ctx context.Context, key string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	info, err := d.statObject(ctx, key)
	if err != nil {
		return 0, err
	}
	return info.Size, nil
}

// LastModified 返回对象的最后修改时间。
func (d *s3Disk) LastModified(ctx context.Context, key string) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}

	info, err := d.statObject(ctx, key)
	if err != nil {
		return time.Time{}, err
	}
	return info.LastModified, nil
}

// MimeType 返回对象的内容类型，桶里没记录时按扩展名推断。
func (d *s3Disk) MimeType(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	info, err := d.statObject(ctx, key)
	if err != nil {
		return "", err
	}
	if info.ContentType != "" {
		return info.ContentType, nil
	}
	if detected := mime.TypeByExtension(path.Ext(key)); detected != "" {
		return detected, nil
	}
	return "application/octet-stream", nil
}

// URL 返回对象的公开访问地址。
//
// 配置了公开地址前缀时以它为准（CDN 或反向代理）。未配置时按 endpoint 与桶名推导出 S3 的
// 规范地址——**它只在桶允许匿名读取时可用**，私有桶请配置公开地址前缀或改用 TemporaryURL。
func (d *s3Disk) URL(key string) (string, error) {
	if d.publicURL != "" {
		return d.publicURL + "/" + keyOf(key), nil
	}

	scheme := lo.Ternary(d.useSSL, "https", "http")
	return fmt.Sprintf("%s://%s/%s/%s", scheme, d.endpoint, d.bucket, keyOf(key)), nil
}

// TemporaryURL 生成带签名的临时下载地址。签名在本地完成，不产生网络往返。
func (d *s3Disk) TemporaryURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if ttl <= 0 {
		return "", fmt.Errorf("storage: presigned download URL ttl must be positive, got %s", ttl)
	}

	u, err := d.client.PresignedGetObject(ctx, d.bucket, keyOf(key), ttl, nil)
	if err != nil {
		return "", opFailed("generate s3 presigned download url", key, err)
	}
	return u.String(), nil
}

// PresignPut 生成带签名的临时上传地址。
func (d *s3Disk) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if ttl <= 0 {
		return "", fmt.Errorf("storage: presigned upload URL ttl must be positive, got %s", ttl)
	}

	u, err := d.client.PresignedPutObject(ctx, d.bucket, keyOf(key), ttl)
	if err != nil {
		return "", opFailed("generate s3 presigned upload url", key, err)
	}
	return u.String(), nil
}

// HealthCheck 探测桶是否可达与存在。超时由调用方的 ctx 决定。
func (d *s3Disk) HealthCheck(ctx context.Context) error {
	exists, err := d.client.BucketExists(ctx, d.bucket)
	if err != nil {
		return fmt.Errorf("storage: failed to probe S3 bucket %s: %w", d.bucket, err)
	}
	if !exists {
		return fmt.Errorf("storage: S3 bucket %s does not exist", d.bucket)
	}
	return nil
}

// Driver 返回当前存储驱动名称。若服务地址包含 garage 则自动识别为 garage，缺省为 s3。
func (d *s3Disk) Driver() string {
	return lo.Ternary(strings.Contains(strings.ToLower(d.endpoint), "garage"), "garage", "s3")
}

// Ping 实现 ready.Pinger 接口。
func (d *s3Disk) Ping(ctx context.Context) error {
	return d.HealthCheck(ctx)
}
