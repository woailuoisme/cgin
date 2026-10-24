// Package media_test 针对媒体文件命名策略与存储路径规划组件进行契约测试。
package media_test

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"bgin/internal/shared/media"
)

func TestNaming_Strategies(t *testing.T) {
	ctx := t.Context()
	content := []byte("hello media content")
	hashExpected := sha256.Sum256(content)
	hashHexExpected := hex.EncodeToString(hashExpected[:])

	t.Run("UUIDNamingStrategy 基于 UUIDv7 生成文件名并保留扩展名", func(t *testing.T) {
		naming := media.NewUUIDNamingStrategy()
		name, hash := naming.GenerateName(ctx, "avatar.png", content)

		assert.Equal(t, hashHexExpected, hash)
		assert.True(t, strings.HasSuffix(name, ".png"))
		baseName := strings.TrimSuffix(name, ".png")
		assert.Len(t, baseName, 36, "UUIDv7 标准字符串长度应为 36")
	})

	t.Run("HashNamingStrategy 生成指定前缀长度的哈希文件名", func(t *testing.T) {
		naming := media.NewHashNamingStrategy(16)
		name, hash := naming.GenerateName(ctx, "document.pdf", content)

		assert.Equal(t, hashHexExpected, hash)
		assert.Equal(t, hashHexExpected[:16]+".pdf", name)
	})

	t.Run("SanitizedOriginalNamingStrategy 清洗原始文件名并追加防碰撞后缀", func(t *testing.T) {
		naming := media.NewSanitizedOriginalNamingStrategy()
		name, hash := naming.GenerateName(ctx, "My Awesome File!.png", content)

		assert.Equal(t, hashHexExpected, hash)
		assert.True(t, strings.HasPrefix(name, "my_awesome_file"))
		assert.True(t, strings.HasSuffix(name, ".png"))
	})
}

func TestPath_Strategies(t *testing.T) {
	ctx := t.Context()
	datePart := time.Now().UTC().Format("20060102")

	t.Run("CategoryDatePathStrategy 按业务类型与日期层级分区", func(t *testing.T) {
		p := media.NewCategoryDatePathStrategy("uploads")
		filePath := p.GeneratePath(ctx, "avatars", "abc.png")

		expected := path.Join("uploads", "avatars", datePart, "abc.png")
		assert.Equal(t, expected, filePath)
	})

	t.Run("CategoryDatePathStrategy 空分类缺省为 default", func(t *testing.T) {
		p := media.NewCategoryDatePathStrategy("uploads")
		filePath := p.GeneratePath(ctx, "", "abc.png")

		expected := path.Join("uploads", "default", datePart, "abc.png")
		assert.Equal(t, expected, filePath)
	})

	t.Run("DatePathStrategy 仅按日期层级分区", func(t *testing.T) {
		p := media.NewDatePathStrategy("assets")
		filePath := p.GeneratePath(ctx, "ignored", "xyz.jpg")

		expected := path.Join("assets", datePart, "xyz.jpg")
		assert.Equal(t, expected, filePath)
	})
}
