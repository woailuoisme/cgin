package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bgin/internal/platform/errs"
)

// localDisk 用操作系统文件系统实现 Disk。
//
// 它没有预签名能力（返回 ErrPresignUnsupported）：本地文件由谁提供、怎么鉴权，属于部署环境
// 的事（静态服务器、反向代理）。公开地址因此必须由配置显式给出，驱动不会凭空拼一个。
// 编译期断言：驱动一旦不再满足 Disk 契约，构建就失败在这里，而不是在 Open 的赋值处。
var _ Disk = (*localDisk)(nil)

type localDisk struct {
	rootPath string
	baseURL  string
}

// NewLocalDisk 以 rootPath 为物理根目录构造本地驱动。目录不存在时创建；
// baseURL 是该根目录对外的公开地址前缀，留空表示没有公开地址。
//
// 所有读写都被限制在 rootPath 之下：调用方给出的键先经 filepath.Clean 与相对路径校验，
// 因此 "../" 无法跳出根目录。注意它**不解析符号链接**——根目录内若存在指向外部的软链，
// 沿链写入仍会落在根目录之外，这需要由目录的属主自行保证。
func NewLocalDisk(rootPath, baseURL string) (Disk, error) {
	if strings.TrimSpace(rootPath) == "" {
		return nil, errors.New("storage: local root path cannot be empty")
	}

	if err := os.MkdirAll(rootPath, 0o750); err != nil {
		return nil, fmt.Errorf("storage: failed to create local root directory %s: %w", rootPath, err)
	}

	return &localDisk{rootPath: rootPath, baseURL: strings.TrimSuffix(baseURL, "/")}, nil
}

// SupportsPresign 显式声明本地文件系统驱动不支持预签名。
func (d *localDisk) SupportsPresign() bool {
	return false
}

// resolvePath 把对象键解析为根目录内的绝对路径，越界或含有非法空字符时返回 400。
func (d *localDisk) resolvePath(key string) (string, error) {
	if strings.ContainsRune(key, 0) {
		return "", errs.BadRequest("The requested storage path is not allowed")
	}

	clean := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(key, "/")))
	full := filepath.Join(d.rootPath, clean)

	rel, err := filepath.Rel(d.rootPath, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errs.BadRequest("The requested storage path is not allowed")
	}
	return full, nil
}

// preparePath 校验上下文有效性并将对象键解析为物理路径。
func (d *localDisk) preparePath(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return d.resolvePath(key)
}

// ensureDir 递归创建目标文件的父目录。
func ensureDir(filePath string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err != nil {
		return fmt.Errorf("storage: failed to create directory %s: %w", filepath.Dir(filePath), err)
	}
	return nil
}

// Put 覆盖写入整个对象，父目录不存在时自动创建。
func (d *localDisk) Put(ctx context.Context, key string, contents []byte) error {
	full, err := d.preparePath(ctx, key)
	if err != nil {
		return err
	}
	if err := ensureDir(full); err != nil {
		return opFailed("write local object", key, err)
	}
	if err := os.WriteFile(full, contents, 0o600); err != nil {
		return opFailed("write local object", key, err)
	}
	return nil
}

// PutStream 从流写入对象，使用内存池化的 64KB 缓冲区减少高并发内存分配与 GC 压力。
func (d *localDisk) PutStream(ctx context.Context, key string, r io.Reader) (err error) {
	full, err := d.preparePath(ctx, key)
	if err != nil {
		return err
	}
	if err := ensureDir(full); err != nil {
		return opFailed("write local object", key, err)
	}

	f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return opFailed("write local object", key, err)
	}
	// 统一在 defer 中关闭：Close 是延迟写入错误的唯一出口，同时覆盖 io.CopyBuffer 提前返回
	// 的路径（不泄漏文件描述符），并避免正常路径上的重复关闭。
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = opFailed("write local object", key, cerr)
		}
	}()

	buf := getCopyBuffer()
	defer putCopyBuffer(buf)

	if _, err := io.CopyBuffer(f, r, *buf); err != nil {
		return opFailed("write local object", key, err)
	}
	return nil
}

// PutMultipart 从 HTTP multipart 表单头写入对象。
func (d *localDisk) PutMultipart(ctx context.Context, key string, file *multipart.FileHeader) error {
	if file == nil {
		return errs.BadRequest("Upload file cannot be nil")
	}
	src, err := file.Open()
	if err != nil {
		return opFailed("open upload file", key, err)
	}
	defer func() { _ = src.Close() }()

	return d.PutStream(ctx, key, src)
}

// Get 读出对象的全部字节。
func (d *localDisk) Get(ctx context.Context, key string) ([]byte, error) {
	full, err := d.preparePath(ctx, key)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, notFound(key)
		}
		return nil, opFailed("read local object", key, err)
	}
	return data, nil
}

// GetStream 返回对象的只读流。
func (d *localDisk) GetStream(ctx context.Context, key string) (io.ReadCloser, error) {
	full, err := d.preparePath(ctx, key)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, notFound(key)
		}
		return nil, opFailed("open local object", key, err)
	}
	return f, nil
}

// Delete 删除对象；对象本就不存在时视为成功，使删除操作天然幂等。
func (d *localDisk) Delete(ctx context.Context, key string) error {
	full, err := d.preparePath(ctx, key)
	if err != nil {
		return err
	}

	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return opFailed("delete local object", key, err)
	}
	return nil
}

// Exists 报告对象是否存在。
func (d *localDisk) Exists(ctx context.Context, key string) (bool, error) {
	full, err := d.preparePath(ctx, key)
	if err != nil {
		return false, err
	}

	if _, err := os.Stat(full); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, opFailed("check local object", key, err)
	}
	return true, nil
}

// Size 返回对象的字节数，目录返回 0。
func (d *localDisk) Size(ctx context.Context, key string) (int64, error) {
	info, err := d.stat(ctx, key, "stat local object size")
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// LastModified 返回对象的最后修改时间。
func (d *localDisk) LastModified(ctx context.Context, key string) (time.Time, error) {
	info, err := d.stat(ctx, key, "stat local object modtime")
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// stat 是 Size 与 LastModified 共用的取元数据路径。
func (d *localDisk) stat(ctx context.Context, key, op string) (os.FileInfo, error) {
	full, err := d.preparePath(ctx, key)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, notFound(key)
		}
		return nil, opFailed(op, key, err)
	}
	return info, nil
}

// MimeType 先按扩展名推断，未知扩展名再读头部字节嗅探，最后回退到通用二进制类型。
func (d *localDisk) MimeType(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if detected := mime.TypeByExtension(filepath.Ext(key)); detected != "" {
		return detected, nil
	}

	f, err := d.GetStream(ctx, key)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if n > 0 {
		return http.DetectContentType(buf[:n]), nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", opFailed("sniff local object mime", key, err)
	}
	return "application/octet-stream", nil
}

// URL 返回对象的公开访问地址，由配置里的公开地址前缀与对象键拼成。
// 没有配置前缀时返回 ErrNoPublicURL，而不是凭空指向一个本进程并未注册的路由。
func (d *localDisk) URL(key string) (string, error) {
	if d.baseURL == "" {
		return "", ErrNoPublicURL
	}
	return d.baseURL + "/" + keyOf(key), nil
}

// TemporaryURL 在 local 驱动上不可用：本地文件没有服务端签名机制。
func (d *localDisk) TemporaryURL(context.Context, string, time.Duration) (string, error) {
	return "", ErrPresignUnsupported
}

// PresignPut 在 local 驱动上不可用，原因同 TemporaryURL。
func (d *localDisk) PresignPut(context.Context, string, time.Duration) (string, error) {
	return "", ErrPresignUnsupported
}

// HealthCheck 确认根目录存在且可写：只读挂载这类问题若留到第一次上传才暴露，
// 排查成本远高于启动即失败。
func (d *localDisk) HealthCheck(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	info, err := os.Stat(d.rootPath)
	if err != nil {
		return fmt.Errorf("storage: local root directory %s is not accessible: %w", d.rootPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("storage: local root path %s is not a directory", d.rootPath)
	}

	probe, err := os.CreateTemp(d.rootPath, ".healthcheck-*")
	if err != nil {
		return fmt.Errorf("storage: local root directory %s is not writable: %w", d.rootPath, err)
	}
	name := probe.Name()
	_ = probe.Close()

	if err := os.Remove(name); err != nil {
		return fmt.Errorf("storage: failed to clean probe file %s: %w", name, err)
	}
	return nil
}

// Driver 返回当前存储驱动名称。
func (d *localDisk) Driver() string {
	return "local"
}

// Ping 实现 ready.Pinger 接口。
func (d *localDisk) Ping(ctx context.Context) error {
	return d.HealthCheck(ctx)
}
