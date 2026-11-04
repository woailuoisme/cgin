// Package media_test 针对 Uploader 预签名上传协调器与 Signer 签名引擎进行契约测试。
package media_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/pkg/media"
	"cgin/pkg/storage"
)

type fakeDisk struct {
	files   map[string][]byte
	deleted map[string]bool
}

func newFakeDisk() *fakeDisk {
	return &fakeDisk{
		files:   make(map[string][]byte),
		deleted: make(map[string]bool),
	}
}

var _ storage.Disk = (*fakeDisk)(nil)
var _ storage.Presigner = (*fakeDisk)(nil)

func (d *fakeDisk) Put(_ context.Context, path string, contents []byte) error {
	d.files[path] = contents
	delete(d.deleted, path)
	return nil
}

func (d *fakeDisk) PutStream(_ context.Context, path string, reader io.Reader) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	d.files[path] = data
	delete(d.deleted, path)
	return nil
}

func (d *fakeDisk) PutMultipart(_ context.Context, _ string, _ *multipart.FileHeader) error {
	return nil
}

func (d *fakeDisk) Get(_ context.Context, path string) ([]byte, error) {
	if data, ok := d.files[path]; ok {
		return data, nil
	}
	return nil, storage.ErrNoPublicURL
}

func (d *fakeDisk) GetStream(_ context.Context, path string) (io.ReadCloser, error) {
	if data, ok := d.files[path]; ok {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return nil, storage.ErrNoPublicURL
}

func (d *fakeDisk) Delete(_ context.Context, path string) error {
	delete(d.files, path)
	d.deleted[path] = true
	return nil
}

func (d *fakeDisk) Exists(_ context.Context, path string) (bool, error) {
	_, ok := d.files[path]
	return ok, nil
}

func (d *fakeDisk) Size(_ context.Context, path string) (int64, error) {
	if data, ok := d.files[path]; ok {
		return int64(len(data)), nil
	}
	return 0, storage.ErrNoPublicURL
}

func (d *fakeDisk) LastModified(_ context.Context, _ string) (time.Time, error) {
	return time.Now(), nil
}

func (d *fakeDisk) MimeType(_ context.Context, _ string) (string, error) {
	return "image/png", nil
}

func (d *fakeDisk) URL(path string) (string, error) {
	return "https://storage.example.com/" + path, nil
}

func (d *fakeDisk) TemporaryURL(_ context.Context, path string, _ time.Duration) (string, error) {
	return "https://storage.example.com/temp/" + path, nil
}

func (d *fakeDisk) PresignPut(_ context.Context, path string, _ time.Duration) (string, error) {
	return "https://storage.example.com/presigned-put/" + path + "?signature=mock", nil
}

func (d *fakeDisk) Ping(_ context.Context) error {
	return nil
}

func (d *fakeDisk) Driver() string {
	return "memory-mock"
}

func setupTestUploader(t *testing.T) (media.Uploader, *fakeDisk) {
	t.Helper()
	disk := newFakeDisk()
	v := media.NewValidator()
	v.MaxFileSize = 20 * 1024 * 1024
	v.AllowedExtensions = []string{"png", "jpg", "jpeg", "webp"}
	v.AllowedMimeTypes = []string{"image/png", "image/jpeg", "image/webp"}

	uploader := media.NewUploader(disk, media.WithValidator(v))
	require.NotNil(t, uploader)
	return uploader, disk
}

func setupTestSigner(t *testing.T) media.Signer {
	t.Helper()
	keyHex := hex.EncodeToString([]byte("test-key-123456789012345678901234"))
	saltHex := hex.EncodeToString([]byte("test-salt-12345678901234567890123"))

	cfg := media.SignerConfig{
		Imgproxy: media.ImgproxyConfig{
			BaseURL:        "https://img.example.com",
			Key:            keyHex,
			Salt:           saltHex,
			Insecure:       false,
			DefaultFormat:  "webp",
			DefaultQuality: 85,
		},
		StorageType: "s3",
		S3Bucket:    "my-bucket",
	}
	return media.NewSigner(cfg)
}

func TestUploader_Presign(t *testing.T) {
	ctx := t.Context()
	uploader, _ := setupTestUploader(t)

	t.Run("有效参数成功签发预签名凭证", func(t *testing.T) {
		res, err := uploader.Presign(ctx, media.PresignRequest{
			Category: "avatars",
			FileName: "my_avatar.png",
			FileSize: 1024 * 1024,
			MimeType: "image/png",
		})
		require.NoError(t, err)
		assert.Equal(t, "PUT", res.Method)
		assert.Equal(t, "image/png", res.Headers["Content-Type"])
		assert.True(t, strings.HasPrefix(res.Path, "uploads/avatars/"))
		assert.True(t, strings.HasSuffix(res.Path, ".png"))
		assert.Contains(t, res.UploadURL, "https://storage.example.com/presigned-put/")
		assert.Equal(t, int64(media.DefaultPresignTTL.Seconds()), res.ExpiresIn)
	})

	t.Run("空文件名被拒绝", func(t *testing.T) {
		_, err := uploader.Presign(ctx, media.PresignRequest{
			Category: "avatars",
			FileName: "",
			FileSize: 1024,
			MimeType: "image/png",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "file name cannot be empty")
	})

	t.Run("非法扩展名被拒绝", func(t *testing.T) {
		_, err := uploader.Presign(ctx, media.PresignRequest{
			Category: "avatars",
			FileName: "hack.sh",
			FileSize: 1024,
			MimeType: "text/x-shellscript",
		})
		require.Error(t, err)
	})

	t.Run("超出全局文件配额被拒绝", func(t *testing.T) {
		_, err := uploader.Presign(ctx, media.PresignRequest{
			Category: "covers",
			FileName: "huge.png",
			FileSize: 30 * 1024 * 1024, // 30MB > 20MB
			MimeType: "image/png",
		})
		require.Error(t, err)
	})

	t.Run("Presigner 为 nil 时安全返回错误而非 Panic", func(t *testing.T) {
		nilUploader := media.NewUploader(nil)
		_, err := nilUploader.Presign(ctx, media.PresignRequest{
			Category: "avatars",
			FileName: "test.png",
			FileSize: 1024,
			MimeType: "image/png",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "storage: driver does not support presigned URLs")
	})
}

func TestSigner_SignAndVariants(t *testing.T) {
	signer := setupTestSigner(t)
	testPath := "uploads/avatars/20261031/uuid.png"

	t.Run("Sign 生成防篡改 URL", func(t *testing.T) {
		url := signer.Sign(testPath, media.PresetAvatarLarge)
		assert.True(t, strings.HasPrefix(url, "https://img.example.com/"))
		assert.Contains(t, url, "/rs:fill:256:256:")
		assert.Contains(t, url, "/g:fp:0:0/")
		assert.True(t, strings.HasSuffix(url, ".webp"))
	})

	t.Run("Variants 生成复合变体集", func(t *testing.T) {
		variants := signer.Variants(testPath)
		require.NotNil(t, variants)
		assert.Equal(t, testPath, variants.Path)
		assert.True(t, strings.HasPrefix(variants.URL, "https://img.example.com/"))
		assert.True(t, strings.HasPrefix(variants.ThumbURL, "https://img.example.com/"))
		assert.Contains(t, variants.ThumbURL, "/rs:fill:64:64:")
	})

	t.Run("BuildImageVariants 使用 Signer 生成变体", func(t *testing.T) {
		variants := media.BuildImageVariants(testPath, signer)
		require.NotNil(t, variants)
		assert.Equal(t, testPath, variants.Path)
		assert.True(t, strings.HasPrefix(variants.URL, "https://img.example.com/"))
		assert.True(t, strings.HasPrefix(variants.ThumbURL, "https://img.example.com/"))
	})

	t.Run("BuildImageVariants 不传 Signer 时仅保留 Path", func(t *testing.T) {
		variants := media.BuildImageVariants(testPath, nil)
		require.NotNil(t, variants)
		assert.Equal(t, testPath, variants.Path)
		assert.Equal(t, testPath, variants.URL)
		assert.Equal(t, testPath, variants.ThumbURL)
	})

	t.Run("空路径 Variants 返回 nil", func(t *testing.T) {
		assert.Nil(t, signer.Variants(""))
		assert.Nil(t, signer.Variants("   "))
	})

	t.Run("SignWithOptions 针对自定义选项构建 URL", func(t *testing.T) {
		url, err := signer.SignWithOptions(testPath,
			media.WithResizingType(media.ResizeFill),
			media.WithResize(100, 100),
		)
		require.NoError(t, err)
		assert.Contains(t, url, "/rs:fill:100:100:")
	})
}

func TestSigner_Inspect(t *testing.T) {
	signer := setupTestSigner(t)

	t.Run("有效 PNG 字节切片成功解析元数据", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 120, 80))
		var buf bytes.Buffer
		err := png.Encode(&buf, img)
		require.NoError(t, err)

		meta, err := signer.Inspect(buf.Bytes())
		require.NoError(t, err)
		require.NotNil(t, meta)
		assert.Equal(t, 120, meta.Width)
		assert.Equal(t, 80, meta.Height)
		assert.Equal(t, media.FormatPNG, meta.Format)
		assert.Equal(t, int64(buf.Len()), meta.Size)
	})

	t.Run("空字节流返回错误", func(t *testing.T) {
		_, err := signer.Inspect(nil)
		require.Error(t, err)
	})
}

func TestUploader_CategoryPolicy(t *testing.T) {
	ctx := t.Context()
	uploader, _ := setupTestUploader(t)

	t.Run("Avatar 类目超出 5MB 上限被拦截", func(t *testing.T) {
		_, err := uploader.Presign(ctx, media.PresignRequest{
			Category: "avatars",
			FileName: "avatar.png",
			FileSize: 6 * 1024 * 1024, // 6MB > 5MB
			MimeType: "image/png",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds maximum limit")
	})

	t.Run("Avatar 类目非允许扩展名被拦截", func(t *testing.T) {
		_, err := uploader.Presign(ctx, media.PresignRequest{
			Category: "avatars",
			FileName: "script.sh",
			FileSize: 1024,
			MimeType: "text/x-shellscript",
		})
		require.Error(t, err)
	})

	t.Run("Avatar 类目非允许 MIME 被拦截", func(t *testing.T) {
		_, err := uploader.Presign(ctx, media.PresignRequest{
			Category: "avatars",
			FileName: "fake.png",
			FileSize: 1024,
			MimeType: "application/pdf",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MIME type")
	})
}

func TestDisk_ExistsAndDelete(t *testing.T) {
	ctx := t.Context()
	disk := newFakeDisk()
	testPath := "uploads/avatars/20261031/test.png"

	// 初始状态不存在
	exists, err := disk.Exists(ctx, testPath)
	require.NoError(t, err)
	assert.False(t, exists)

	// 写入后存在
	_ = disk.Put(ctx, testPath, []byte("data"))
	exists, err = disk.Exists(ctx, testPath)
	require.NoError(t, err)
	assert.True(t, exists)

	// 删除后不存在
	err = disk.Delete(ctx, testPath)
	require.NoError(t, err)
	assert.True(t, disk.deleted[testPath])
	exists, err = disk.Exists(ctx, testPath)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestSigner_Ping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("imgproxy is running"))
	}))
	t.Cleanup(server.Close)

	pingSigner := media.NewSigner(media.SignerConfig{
		Imgproxy: media.ImgproxyConfig{
			BaseURL:     server.URL,
			InternalURL: server.URL,
			Insecure:    true,
		},
	})
	assert.Equal(t, "imgproxy", pingSigner.Driver())
	require.NoError(t, pingSigner.Ping(t.Context()))
}
