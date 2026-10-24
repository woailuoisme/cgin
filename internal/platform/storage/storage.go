// Package storage 提供统一对象存储抽象（Disk）及本地文件系统与 S3 驱动实现。
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"path"
	"strings"
	"time"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
)

// msgFileNotFound 是对象不存在时对外暴露的固定文案。对象键不进响应体，只留在因由里进日志。
const msgFileNotFound = "The requested file does not exist"

// 驱动能力的边界用哨兵错误表达，便于调用方 errors.Is 判断，而不是靠匹配字符串。
var (
	// ErrNoPublicURL 表示没有配置公开访问地址，因此给不出永久链接。
	ErrNoPublicURL = errors.New("storage: no public URL configured")

	// ErrPresignUnsupported 表示当前驱动不支持生成预签名 URL。
	ErrPresignUnsupported = errors.New("storage: driver does not support presigned URLs")
)

// Reader 定义读取对象的角色契约。
type Reader interface {
	// Get 读出对象的全部字节。
	Get(ctx context.Context, path string) ([]byte, error)
	// GetStream 返回对象的只读流，由调用方负责关闭。
	GetStream(ctx context.Context, path string) (io.ReadCloser, error)
}

// Writer 定义写入对象的角色契约。
type Writer interface {
	// Put 覆盖写入整个对象。
	Put(ctx context.Context, path string, contents []byte) error
	// PutStream 从流写入对象；读取长度未知时由驱动自行决定分片策略。
	PutStream(ctx context.Context, path string, r io.Reader) error
	// PutMultipart 从 HTTP multipart 表单头写入对象，自动管理流生命周期与资源释放。
	PutMultipart(ctx context.Context, path string, file *multipart.FileHeader) error
}

// Deleter 定义删除对象的角色契约。
type Deleter interface {
	// Delete 删除对象；对象本就不存在时视为成功。
	Delete(ctx context.Context, path string) error
}

// Inspector 定义查询对象属性与存在性的元数据角色契约。
type Inspector interface {
	// Exists 报告对象是否存在。
	Exists(ctx context.Context, path string) (bool, error)
	// Size 返回对象的字节数。
	Size(ctx context.Context, path string) (int64, error)
	// LastModified 返回对象的最后修改时间。
	LastModified(ctx context.Context, path string) (time.Time, error)
	// MimeType 返回对象的内容类型，先按扩展名推断，再回退到内容嗅探。
	MimeType(ctx context.Context, path string) (string, error)
}

// PublicURLer 定义提供永久公开可访问地址的角色契约。
type PublicURLer interface {
	// URL 返回对象的公开访问地址；未配置公开地址时返回 ErrNoPublicURL。
	URL(path string) (string, error)
}

// Presigner 定义提供带签名与时效的时效性地址的角色契约。
type Presigner interface {
	// TemporaryURL 返回带签名与时效的下载地址；驱动不支持时返回 ErrPresignUnsupported。
	TemporaryURL(ctx context.Context, path string, ttl time.Duration) (string, error)
	// PresignPut 返回带签名与时效的上传地址；驱动不支持时返回 ErrPresignUnsupported。
	PresignPut(ctx context.Context, path string, ttl time.Duration) (string, error)
}

// Pinger 定义存储服务可用性与健康检测的角色契约。
type Pinger interface {
	// HealthCheck 探测存储是否可用，供启动期与就绪探针使用。
	HealthCheck(ctx context.Context) error
	// Ping 探测存储可用性（满足 ready.Pinger 契约）。
	Ping(ctx context.Context) error
}

// Disk 聚合存储读写、属性探查、公开链接、预签名与健康探测，是对象存储基础设施的完整契约。
type Disk interface {
	Reader
	Writer
	Deleter
	Inspector
	PublicURLer
	Presigner
	Pinger
	// Driver 返回当前存储驱动名称（如 local、s3、garage）。
	Driver() string
}

// PresignChecker 是能够显式报告是否支持预签名的驱动扩展契约。
type PresignChecker interface {
	SupportsPresign() bool
}

// SupportsPresign 探查目标 Disk 实例是否具备预签名能力。
func SupportsPresign(d Disk) bool {
	if d == nil {
		return false
	}
	if c, ok := d.(PresignChecker); ok {
		return c.SupportsPresign()
	}
	// 默认 S3 驱动支持预签名，本地文件驱动不支持
	return d.Driver() != config.StorageDriverLocal
}

// AsPresigner 尝试将 Disk 转换为具备预签名能力的 Presigner 角色接口。
func AsPresigner(d Disk) (Presigner, bool) {
	if !SupportsPresign(d) {
		return nil, false
	}
	p, ok := d.(Presigner)
	return p, ok
}

// Open 根据配置构造存储驱动并执行可用性健康探测。
func Open(ctx context.Context, cfg config.StorageConfig) (Disk, error) {
	disk, err := createDriver(cfg)
	if err != nil {
		return nil, err
	}

	if err := disk.HealthCheck(ctx); err != nil {
		return nil, fmt.Errorf("storage driver %s is unavailable: %w", cfg.Driver, err)
	}
	return disk, nil
}

// keyOf 把对象键规整成两个驱动共用的形态：以 "/" 开头、去掉前导斜杠、消解 "." 与 ".."。
// 它只做规范化，不承担越界判定——本地驱动的越界由 resolvePath 用相对路径校验拦截。
func keyOf(key string) string {
	return strings.TrimPrefix(path.Clean("/"+key), "/")
}

// notFound 构造对象不存在的 404：对外文案固定，对象键只进日志。
func notFound(path string) error {
	return errs.Wrap(errs.CodeNotFound, msgFileNotFound, fmt.Errorf("storage object %s does not exist", path))
}

// opFailed 把底层失败连同操作名与对象键收敛为 500。对外文案固定为通用提示，
// 操作名与键只进日志（errs.Internal 的语义）。
func opFailed(op, path string, err error) error {
	return errs.Internal(fmt.Errorf("%s %q failed: %w", op, path, err))
}
