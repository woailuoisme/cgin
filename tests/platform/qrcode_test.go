package platform_test

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/qrcode"
)

// PNG 图像头部固定魔数。
var pngMagicHeader = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// createTestLogo 生成一个指定尺寸的测试用 RGBA 纯色 Logo 图片字节流。
func createTestLogo(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	require.NoError(t, err)
	return buf.Bytes()
}

// TestQRCode_BasicPNG 验证基础黑白二维码的生成与标准 PNG 头解析。
func TestQRCode_BasicPNG(t *testing.T) {
	content := "https://github.com/ailuoga/gin-bun"

	data, err := qrcode.New(content).
		WithSize(200).
		PNG()

	require.NoError(t, err)
	require.NotEmpty(t, data)
	assert.True(t, bytes.HasPrefix(data, pngMagicHeader), "output must be a valid PNG image")

	// 验证可以被标准图像库反序列化
	img, format, err := image.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, "png", format)
	assert.Positive(t, img.Bounds().Dx())
	assert.Positive(t, img.Bounds().Dy())
}

// TestQRCode_Base64DataURI 验证 Base64 Data URI 的前缀及数据完整性。
func TestQRCode_Base64DataURI(t *testing.T) {
	content := "weixin://wxpay/bizpayurl?pr=sample123"

	uri, err := qrcode.New(content).
		WithSize(150).
		Base64DataURI()

	require.NoError(t, err)
	require.NotEmpty(t, uri)

	const expectedPrefix = "data:image/png;base64,"
	require.True(t, strings.HasPrefix(uri, expectedPrefix))

	rawB64 := strings.TrimPrefix(uri, expectedPrefix)
	decodedBytes, err := base64.StdEncoding.DecodeString(rawB64)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(decodedBytes, pngMagicHeader))
}

// TestQRCode_EmptyContent 验证当文本内容为空时的防御性错误校验。
func TestQRCode_EmptyContent(t *testing.T) {
	b := qrcode.New("")

	_, err := b.PNG()
	require.ErrorIs(t, err, qrcode.ErrEmptyContent)

	_, err = b.Base64DataURI()
	require.ErrorIs(t, err, qrcode.ErrEmptyContent)

	var buf bytes.Buffer
	n, err := b.WriteTo(&buf)
	require.ErrorIs(t, err, qrcode.ErrEmptyContent)
	assert.Zero(t, n)
}

// TestQRCode_WriteTo 验证流式写出与空写入器保护。
func TestQRCode_WriteTo(t *testing.T) {
	b := qrcode.New("otpauth://totp/bgin:alice@example.com?secret=JBSWY3DPEHPK3PXP")

	// 验证空写入器错误保护
	n, err := b.WriteTo(nil)
	require.Error(t, err)
	assert.Zero(t, n)

	// 验证流式写出成功
	var buf bytes.Buffer
	n, err = b.WriteTo(&buf)
	require.NoError(t, err)
	assert.Positive(t, n)
	assert.True(t, bytes.HasPrefix(buf.Bytes(), pngMagicHeader))
}

// TestQRCode_CustomStyling 验证圆角、前景色、透明背景与边缘设置。
func TestQRCode_CustomStyling(t *testing.T) {
	content := "bgin-custom-style-test"

	data, err := qrcode.New(content).
		WithCircleShape().
		WithTransparentBackground().
		WithColors(color.RGBA{R: 20, G: 80, B: 200, A: 255}, nil).
		WithBorder(2).
		WithCellWidth(6).
		PNG()

	require.NoError(t, err)
	require.NotEmpty(t, data)
	assert.True(t, bytes.HasPrefix(data, pngMagicHeader))
}

// TestQRCode_WithLogo 验证中心 Logo 嵌入及容错级别自动提升。
func TestQRCode_WithLogo(t *testing.T) {
	content := "https://example.com/checkout/order-8888"
	logoBytes := createTestLogo(t, 40, 40)

	// 1. 正常挂载 Logo 字节切片
	data, err := qrcode.New(content).
		WithSize(300).
		WithLogoBytes(logoBytes).
		PNG()

	require.NoError(t, err)
	require.NotEmpty(t, data)
	assert.True(t, bytes.HasPrefix(data, pngMagicHeader))

	// 2. 挂载已解码的 image.Image 对象
	logoImg, _, err := image.Decode(bytes.NewReader(logoBytes))
	require.NoError(t, err)

	uri, err := qrcode.New(content).
		WithSize(300).
		WithLogoImage(logoImg).
		Base64DataURI()

	require.NoError(t, err)
	require.NotEmpty(t, uri)

	// 3. 挂载非法/空的 Logo 数据时应当报错
	_, err = qrcode.New(content).
		WithLogoBytes([]byte("invalid image content")).
		PNG()
	require.ErrorIs(t, err, qrcode.ErrInvalidLogo)

	_, err = qrcode.New(content).
		WithLogoImage(nil).
		PNG()
	require.ErrorIs(t, err, qrcode.ErrInvalidLogo)
}

// TestQRCode_GenerateHelpers 验证顶层快捷函数。
func TestQRCode_GenerateHelpers(t *testing.T) {
	content := "quick-helper-test"

	// 快捷 PNG
	pngData, err := qrcode.GeneratePNG(content,
		func(b *qrcode.Builder) {
			b.WithSize(180).WithCircleShape()
		},
	)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(pngData, pngMagicHeader))

	// 快捷 Base64
	base64URI, err := qrcode.GenerateBase64(content)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(base64URI, "data:image/png;base64,"))
}
