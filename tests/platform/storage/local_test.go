// 本文件覆盖本地驱动的完整行为契约。
//
// 重点在两处容易被忽略的边界：键里的 ".." 不能跳出根目录，以及公开地址与预签名这两件
// 本地驱动做不到的事必须明确失败，而不是返回一个看似可用的地址。
package storage_test

import (
	"bytes"
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/storage"
)

// newLocalDisk 在临时目录上构造本地驱动，回传根目录便于直接检查落盘结果。
func newLocalDisk(t *testing.T, baseURL string) (storage.Disk, string) {
	t.Helper()

	root := t.TempDir()
	disk, err := storage.NewLocalDisk(root, baseURL)
	require.NoError(t, err)
	return disk, root
}

// TestLocalRoundTrip 覆盖写入、读取、探测与删除的完整往返。
func TestLocalRoundTrip(t *testing.T) {
	disk, root := newLocalDisk(t, "")
	ctx := t.Context()
	key := "users/42/avatar.png"
	content := []byte("\x89PNG\r\n\x1a\n 内容")

	require.NoError(t, disk.Put(ctx, key, content))

	// 键里的目录会被自动创建。
	_, err := os.Stat(filepath.Join(root, "users", "42", "avatar.png"))
	require.NoError(t, err, "写入后应当在根目录下看到该文件")

	got, err := disk.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, content, got)

	exists, err := disk.Exists(ctx, key)
	require.NoError(t, err)
	assert.True(t, exists)

	size, err := disk.Size(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), size)

	modified, err := disk.LastModified(ctx, key)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), modified, time.Minute)

	mimeType, err := disk.MimeType(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "image/png", mimeType)

	require.NoError(t, disk.Delete(ctx, key))
	exists, err = disk.Exists(ctx, key)
	require.NoError(t, err)
	assert.False(t, exists)

	// 删除不存在的对象视为成功，使调用方无需先探测。
	assert.NoError(t, disk.Delete(ctx, key))
}

// TestLocalPutStream 覆盖流式写入与流式读取，并确认读取方负责关闭的流确实可关。
func TestLocalPutStream(t *testing.T) {
	disk, _ := newLocalDisk(t, "")
	ctx := t.Context()
	payload := bytes.Repeat([]byte("stream-"), 128)

	require.NoError(t, disk.PutStream(ctx, "export/dump.bin", bytes.NewReader(payload)))

	stream, err := disk.GetStream(ctx, "export/dump.bin")
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	buf := new(bytes.Buffer)
	_, err = buf.ReadFrom(stream)
	require.NoError(t, err)
	assert.Equal(t, payload, buf.Bytes())
}

// TestLocalMissingObjectIsNotFound 锁定"对象不存在"必须是一个可判别的 404，
// 而不是让上层拿到一个无从分辨的内部错误。
func TestLocalMissingObjectIsNotFound(t *testing.T) {
	disk, _ := newLocalDisk(t, "")
	ctx := t.Context()

	_, err := disk.Get(ctx, "nope/missing.png")
	require.Error(t, err)

	code, ok := errs.As(err)
	require.True(t, ok, "错误应当携带业务错误码")
	assert.Equal(t, errs.CodeNotFound, code.Code())
	assert.NotContains(t, err.Error(), "nope/missing.png", "对象键不应出现在对外文案里")
}

// TestLocalBackslashIsLiteralFilename 记录一个平台差异：POSIX 上反斜杠是合法的文件名字符，
// 因此 "..\..\x" 并不构成路径穿越，它只会成为根目录内一个名字奇怪的文件；Windows 上
// filepath.Clean 把它当分隔符，越界校验会直接拦下。两种语义都可接受，关键是都不会跳出根目录。
func TestLocalBackslashIsLiteralFilename(t *testing.T) {
	disk, root := newLocalDisk(t, "")
	ctx := t.Context()
	key := `..\..\escaped.txt`

	if err := disk.Put(ctx, key, []byte("x")); err != nil {
		_, ok := errs.As(err)
		assert.True(t, ok, "即便拒绝，也应当是带错误码的业务错误")
		return
	}

	_, err := os.Stat(filepath.Join(root, key))
	require.NoError(t, err, "文件应当落在根目录内")

	_, err = os.Stat(filepath.Join(filepath.Dir(root), "escaped.txt"))
	assert.True(t, os.IsNotExist(err), "无论平台如何，都不能跳出根目录")
}

// TestLocalPathTraversalIsRejected 确认 "../" 无法把读写带出根目录。
func TestLocalPathTraversalIsRejected(t *testing.T) {
	disk, root := newLocalDisk(t, "")
	ctx := t.Context()

	for _, key := range []string{"../../escaped.txt", "a/../../../escaped.txt"} {
		t.Run(key, func(t *testing.T) {
			err := disk.Put(ctx, key, []byte("x"))
			require.Error(t, err)

			code, ok := errs.As(err)
			require.True(t, ok)
			assert.Equal(t, errs.CodeBadRequest, code.Code())

			// 根目录的上一级不应出现任何新文件。
			_, statErr := os.Stat(filepath.Join(filepath.Dir(root), "escaped.txt"))
			assert.True(t, os.IsNotExist(statErr), "越界写入必须被拦下")
		})
	}
}

// TestLocalLeadingSlashStaysInsideRoot 确认前导斜杠只会被规整掉，不会被当成绝对路径。
func TestLocalLeadingSlashStaysInsideRoot(t *testing.T) {
	disk, root := newLocalDisk(t, "")
	ctx := t.Context()

	require.NoError(t, disk.Put(ctx, "/profile/avatar.png", []byte("data")))

	// 带斜杠与不带斜杠指向同一个对象。
	got, err := disk.Get(ctx, "profile/avatar.png")
	require.NoError(t, err)
	assert.Equal(t, []byte("data"), got)

	_, err = os.Stat(filepath.Join(root, "profile", "avatar.png"))
	assert.NoError(t, err)
}

// TestLocalURL 确认公开地址由配置前缀拼出，未配置时不凭空造一个。
func TestLocalURL(t *testing.T) {
	t.Run("未配置前缀时报错", func(t *testing.T) {
		disk, _ := newLocalDisk(t, "")

		_, err := disk.URL("users/1/a.png")
		assert.ErrorIs(t, err, storage.ErrNoPublicURL)
	})

	t.Run("配置前缀后拼接", func(t *testing.T) {
		// 结尾多余的斜杠会被规整，避免出现双斜杠。
		disk, _ := newLocalDisk(t, "https://cdn.example.com/assets/")

		url, err := disk.URL("users/1/a.png")
		require.NoError(t, err)
		assert.Equal(t, "https://cdn.example.com/assets/users/1/a.png", url)

		// 键里的相对片段会被规整，不会拼出回退路径。
		url, err = disk.URL("/users/../users/2/b.png")
		require.NoError(t, err)
		assert.Equal(t, "https://cdn.example.com/assets/users/2/b.png", url)
	})
}

// TestLocalPresignIsUnsupported 确认本地驱动明确拒绝预签名，而不是返回无签名的假链接。
func TestLocalPresignIsUnsupported(t *testing.T) {
	disk, _ := newLocalDisk(t, "https://cdn.example.com")
	ctx := t.Context()

	_, err := disk.TemporaryURL(ctx, "users/1/a.png", time.Minute)
	require.ErrorIs(t, err, storage.ErrPresignUnsupported)

	_, err = disk.PresignPut(ctx, "users/1/a.png", time.Minute)
	require.ErrorIs(t, err, storage.ErrPresignUnsupported)
}

// TestNewLocalDiskCreatesRoot 确认根目录会被创建，空路径被拒绝。
func TestNewLocalDiskCreatesRoot(t *testing.T) {
	t.Run("空根目录被拒绝", func(t *testing.T) {
		_, err := storage.NewLocalDisk("  ", "")
		assert.Error(t, err)
	})

	t.Run("缺失的根目录被创建", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "nested", "app")

		_, err := storage.NewLocalDisk(root, "")
		require.NoError(t, err)

		info, err := os.Stat(root)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})
}

// TestLocalHealthCheck 确认健康检查能发现根目录消失这类"启动后环境被改坏"的情况。
func TestLocalHealthCheck(t *testing.T) {
	disk, root := newLocalDisk(t, "")
	ctx := t.Context()

	require.NoError(t, disk.HealthCheck(ctx))

	require.NoError(t, os.RemoveAll(root))
	assert.Error(t, disk.HealthCheck(ctx), "根目录消失后健康检查必须失败")
}

// TestLocalPutMultipart 覆盖从 multipart.FileHeader 保存文件及 nil 防御。
func TestLocalPutMultipart(t *testing.T) {
	disk, _ := newLocalDisk(t, "")
	ctx := t.Context()

	t.Run("nil 文件返回 400", func(t *testing.T) {
		err := disk.PutMultipart(ctx, "avatars/nil.png", nil)
		require.Error(t, err)
		code, ok := errs.As(err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeBadRequest, code.Code())
	})

	t.Run("成功写入 multipart 文件", func(t *testing.T) {
		content := []byte("fake png content")
		header := createMultipartFileHeader(t, "avatar", "test.png", content)
		key := "avatars/user-1.png"

		require.NoError(t, disk.PutMultipart(ctx, key, header))

		got, err := disk.Get(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, content, got)
	})
}

// createMultipartFileHeader 构造测试用的 multipart.FileHeader。
func createMultipartFileHeader(t *testing.T, fieldName, fileName string, content []byte) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, fileName)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	reader := multipart.NewReader(&body, writer.Boundary())
	form, err := reader.ReadForm(int64(body.Len()))
	require.NoError(t, err)
	files := form.File[fieldName]
	require.NotEmpty(t, files)
	return files[0]
}
