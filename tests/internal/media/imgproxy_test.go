// Package media_test 针对 Imgproxy 图像处理客户端、选项构建器与防篡改签名进行契约测试。
package media_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/internal/media"
)

func TestImgproxyClient_Signature(t *testing.T) {
	// 使用固定的测试密钥与 salt (hex 编码)
	keyHex := hex.EncodeToString([]byte("test-key-123456789012345678901234"))
	saltHex := hex.EncodeToString([]byte("test-salt-12345678901234567890123"))

	cfg := config.ImgproxyConfig{
		BaseURL:        "https://img.example.com",
		Key:            keyHex,
		Salt:           saltHex,
		Insecure:       false,
		DefaultFormat:  "webp",
		DefaultQuality: 80,
	}

	client := media.NewImageClient(cfg)
	require.NotNil(t, client)

	t.Run("空输入路径返回错误", func(t *testing.T) {
		_, err := client.URL("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "source uri cannot be empty")
	})

	t.Run("缩略图选项生成合法的签名 URL", func(t *testing.T) {
		thumbURL, err := client.URL("https://storage.example.com/avatars/user.png",
			media.WithResizingType(media.ResizeFill),
			media.WithResize(200, 200),
		)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(thumbURL, "https://img.example.com/"))
		assert.Contains(t, thumbURL, "/rs:fill:200:200:")
		assert.Contains(t, thumbURL, "/q:80/")
		assert.True(t, strings.HasSuffix(thumbURL, ".webp"))
		// 非 insecure 模式下不应包含 insecure 前缀
		assert.NotContains(t, thumbURL, "/insecure/")
	})

	t.Run("头像选项生成带人脸居中的签名 URL", func(t *testing.T) {
		avatarURL, err := client.URL("https://storage.example.com/avatars/user.png",
			media.WithResizingType(media.ResizeFill),
			media.WithResize(120, 120),
			media.WithGravity(media.GravityFocusPoint),
		)
		require.NoError(t, err)
		assert.Contains(t, avatarURL, "/rs:fill:120:120:")
		assert.Contains(t, avatarURL, "/g:fp:")
		assert.True(t, strings.HasSuffix(avatarURL, ".webp"))
	})

	t.Run("Insecure 模式生成带 insecure 前缀的 URL", func(t *testing.T) {
		insecureCfg := config.ImgproxyConfig{
			BaseURL:  "https://img.example.com",
			Insecure: true,
		}
		insecureClient := media.NewImageClient(insecureCfg)
		urlStr, err := insecureClient.URL("https://example.com/test.jpg",
			media.WithResizingType(media.ResizeFill),
			media.WithResize(100, 100),
		)
		require.NoError(t, err)
		assert.Contains(t, urlStr, "https://img.example.com/insecure/")
	})
}

func TestImgproxyClient_ReadyContract(t *testing.T) {
	t.Run("默认配置下报告未启用且驱动为 imgproxy", func(t *testing.T) {
		cfg := config.ImgproxyConfig{
			BaseURL: "http://localhost:8088",
			Enabled: false,
		}
		c := media.NewImageClient(cfg)
		assert.Equal(t, "imgproxy", c.Driver())
		assert.False(t, c.Enabled())
	})

	t.Run("显式启用后报告已启用", func(t *testing.T) {
		cfg := config.ImgproxyConfig{
			BaseURL: "http://localhost:8088",
			Enabled: true,
		}
		c := media.NewImageClient(cfg)
		assert.Equal(t, "imgproxy", c.Driver())
		assert.True(t, c.Enabled())
	})
}
