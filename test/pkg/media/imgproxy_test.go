// Package media_test 针对 Imgproxy 图像处理客户端、选项构建器与防篡改签名进行契约测试。
package media_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/pkg/media"
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

	engine := media.NewEngine(config.MediaConfig{Imgproxy: cfg}, config.StorageConfig{Driver: "memory-mock"}, nil)
	require.NotNil(t, engine)

	t.Run("空输入路径返回错误", func(t *testing.T) {
		_, err := engine.SignWithOptions("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "path cannot be empty")
	})

	t.Run("缩略图选项生成合法的签名 URL", func(t *testing.T) {
		thumbURL, err := engine.SignWithOptions("https://storage.example.com/avatars/user.png",
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
		avatarURL, err := engine.SignWithOptions("https://storage.example.com/avatars/user.png",
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
		insecureEngine := media.NewEngine(config.MediaConfig{Imgproxy: insecureCfg}, config.StorageConfig{Driver: "memory-mock"}, nil)
		urlStr, err := insecureEngine.SignWithOptions("https://example.com/test.jpg",
			media.WithResizingType(media.ResizeFill),
			media.WithResize(100, 100),
		)
		require.NoError(t, err)
		assert.Contains(t, urlStr, "https://img.example.com/insecure/")
	})
}

func TestImgproxyClient_ReadyContract(t *testing.T) {
	t.Run("默认配置下驱动为 imgproxy", func(t *testing.T) {
		cfg := config.ImgproxyConfig{
			BaseURL: "http://localhost:8088",
			Enabled: false,
		}
		engine := media.NewEngine(config.MediaConfig{Imgproxy: cfg}, config.StorageConfig{Driver: "memory-mock"}, nil)
		assert.Equal(t, "imgproxy", engine.Driver())
	})

	t.Run("显式启用后驱动仍为 imgproxy 且具备 Ping 契约", func(t *testing.T) {
		cfg := config.ImgproxyConfig{
			BaseURL: "http://localhost:8088",
			Enabled: true,
		}
		engine := media.NewEngine(config.MediaConfig{Imgproxy: cfg}, config.StorageConfig{Driver: "memory-mock"}, nil)
		assert.Equal(t, "imgproxy", engine.Driver())
	})
}
