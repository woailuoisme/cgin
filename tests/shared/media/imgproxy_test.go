// Package media_test 针对 Imgproxy 图像处理客户端、选项构建器与防篡改签名进行契约测试。
package media_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/shared/media"
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

	t.Run("Thumbnail 生成合法的签名 URL", func(t *testing.T) {
		thumbURL, err := client.Thumbnail("https://storage.example.com/avatars/user.png", 200, 200)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(thumbURL, "https://img.example.com/"))
		assert.Contains(t, thumbURL, "/rs:fill:200:200:")
		assert.Contains(t, thumbURL, "/q:80/")
		assert.True(t, strings.HasSuffix(thumbURL, ".webp"))
		// 非 insecure 模式下不应包含 insecure 前缀
		assert.NotContains(t, thumbURL, "/insecure/")
	})

	t.Run("Avatar 生成带人脸居中的签名 URL", func(t *testing.T) {
		avatarURL, err := client.Avatar("https://storage.example.com/avatars/user.png", 120)
		require.NoError(t, err)
		assert.Contains(t, avatarURL, "/rs:fill:120:120:")
		assert.Contains(t, avatarURL, "/g:fp:")
		assert.True(t, strings.HasSuffix(avatarURL, ".webp"))
	})

	t.Run("Builder 链式构建生成 URL", func(t *testing.T) {
		builtURL, err := client.Build("https://storage.example.com/photos/banner.jpg").
			Resize(800, 600).
			Format(media.FormatAVIF).
			Quality(90).
			Blur(1.5).
			Sharpen(0.8).
			Sign()

		require.NoError(t, err)
		assert.Contains(t, builtURL, "/rs:fit:800:600:")
		assert.Contains(t, builtURL, "/q:90/")
		assert.Contains(t, builtURL, "/bl:1.50/")
		assert.Contains(t, builtURL, "/sh:0.80/")
		assert.True(t, strings.HasSuffix(builtURL, ".avif"))
	})

	t.Run("Insecure 模式生成带 insecure 前缀的 URL", func(t *testing.T) {
		insecureCfg := config.ImgproxyConfig{
			BaseURL:  "https://img.example.com",
			Insecure: true,
		}
		insecureClient := media.NewImageClient(insecureCfg)
		urlStr, err := insecureClient.Thumbnail("https://example.com/test.jpg", 100, 100)
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
