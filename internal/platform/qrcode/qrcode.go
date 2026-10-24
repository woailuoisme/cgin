// Package qrcode 提供高内聚、易扩展的企业级二维码生成与渲染组件。
// 支持标准黑白码、中心 Logo 挂载、圆角形状、透明底、PNG 二进制流与 Base64 Data URI 输出。
package qrcode

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"

	"github.com/yeqown/go-qrcode/v2"
	"github.com/yeqown/go-qrcode/writer/standard"
	_ "golang.org/x/image/webp"
)

// 模块内置哨兵错误。
var (
	// ErrEmptyContent 当输入的待转码文本为空时返回。
	ErrEmptyContent = errors.New("qrcode: content cannot be empty")
	// ErrInvalidLogo 当传入的 Logo 字节或图片无法解析时返回。
	ErrInvalidLogo = errors.New("qrcode: invalid logo image data")
	// ErrEncodeFailed 二维码矩阵编码失败。
	ErrEncodeFailed = errors.New("qrcode: failed to encode qr matrix")
	// ErrWriteFailed 二维码图形渲染写出失败。
	ErrWriteFailed = errors.New("qrcode: failed to write qr code")
)

// ErrorCorrectionLevel 二维码容错恢复能力级别。
type ErrorCorrectionLevel int

const (
	// ErrorCorrectionDefault 默认容错级别（自动回退为 Medium 约 15% 容错率）。
	ErrorCorrectionDefault ErrorCorrectionLevel = iota
	// ErrorCorrectionLow 约 7% 容错率。
	ErrorCorrectionLow
	// ErrorCorrectionMedium 约 15% 容错率（常规默认值）。
	ErrorCorrectionMedium
	// ErrorCorrectionQuart 约 25% 容错率。
	ErrorCorrectionQuart
	// ErrorCorrectionHighest 约 30% 容错率（挂载 Logo 时自动升级至此级别）。
	ErrorCorrectionHighest
)

// Option 定义快捷函数配置函数项。
type Option func(*Builder)

// Builder 封装二维码流式链式构建器。
type Builder struct {
	content       string
	level         ErrorCorrectionLevel
	hasExplicitEC bool
	sizeHint      int
	cellWidth     uint8
	border        int
	fgColor       color.Color
	bgColor       color.Color
	bgTransparent bool
	circleShape   bool
	logoImage     image.Image
	err           error
}

var _ io.WriterTo = (*Builder)(nil)

// New 实例化一个二维码构建器。
func New(content string) *Builder {
	b := &Builder{
		content:  content,
		level:    ErrorCorrectionMedium,
		sizeHint: 256,
		border:   1,
	}
	if content == "" {
		b.err = ErrEmptyContent
	}
	return b
}

// WithSize 设置推荐的目标像素尺寸（例如 256、512），构建器将根据二维码矩阵规模自适应估算块大小。
func (b *Builder) WithSize(size int) *Builder {
	if size > 0 {
		b.sizeHint = size
	}
	return b
}

// WithCellWidth 显式设置二维码每个单元格（Module）的像素宽度。优先级高于 WithSize。
func (b *Builder) WithCellWidth(width uint8) *Builder {
	b.cellWidth = width
	return b
}

// WithBorder 设置四周边缘留白格数（默认为 1）。
func (b *Builder) WithBorder(border int) *Builder {
	if border >= 0 {
		b.border = border
	}
	return b
}

// WithErrorCorrection 显式指定容错级别。
func (b *Builder) WithErrorCorrection(level ErrorCorrectionLevel) *Builder {
	b.level = level
	b.hasExplicitEC = true
	return b
}

// WithColors 配置二维码前景色与背景色。
func (b *Builder) WithColors(fg, bg color.Color) *Builder {
	if fg != nil {
		b.fgColor = fg
	}
	if bg != nil {
		b.bgColor = bg
	}
	return b
}

// WithTransparentBackground 设置背景为透明底（常用于海报/UI合成）。
func (b *Builder) WithTransparentBackground() *Builder {
	b.bgTransparent = true
	return b
}

// WithCircleShape 启用圆角点阵风格。
func (b *Builder) WithCircleShape() *Builder {
	b.circleShape = true
	return b
}

// WithLogoImage 挂载已解码的内存 Logo 图像对象。
// 为保证被遮挡区域的可读性，若未显式指定容错级别，将自动提升至 ErrorCorrectionHighest。
func (b *Builder) WithLogoImage(img image.Image) *Builder {
	if img == nil {
		b.err = errors.Join(b.err, ErrInvalidLogo)
		return b
	}
	b.logoImage = img
	if !b.hasExplicitEC {
		b.level = ErrorCorrectionHighest
	}
	return b
}

// WithLogoBytes 解析图片二进制切片并挂载中心 Logo（支持 PNG、JPEG、WEBP）。
func (b *Builder) WithLogoBytes(data []byte) *Builder {
	if len(data) == 0 {
		b.err = errors.Join(b.err, ErrInvalidLogo)
		return b
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		b.err = errors.Join(b.err, fmt.Errorf("%w: %w", ErrInvalidLogo, err))
		return b
	}
	return b.WithLogoImage(img)
}

// WithLogoFile 加载本地磁盘文件并挂载中心 Logo。
func (b *Builder) WithLogoFile(filePath string) *Builder {
	if filePath == "" {
		b.err = errors.Join(b.err, ErrInvalidLogo)
		return b
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		b.err = errors.Join(b.err, fmt.Errorf("%w: %w", ErrInvalidLogo, err))
		return b
	}
	return b.WithLogoBytes(data)
}

// WriteTo 实现 io.WriterTo 接口，将生成的 PNG 二维码直接流式写出到给定的 io.Writer 中。
func (b *Builder) WriteTo(w io.Writer) (int64, error) {
	if b.err != nil {
		return 0, b.err
	}
	if w == nil {
		return 0, errors.New("qrcode: writer cannot be nil")
	}

	qrc, err := qrcode.NewWith(b.content, toQRErrorCorrection(b.level))
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrEncodeFailed, err)
	}

	cw := &countingWriteCloser{w: w}
	writerOpts := b.buildWriterOptions(qrc.Dimension())
	sw := standard.NewWithWriter(cw, writerOpts...)

	if err := qrc.Save(sw); err != nil {
		return cw.n, fmt.Errorf("%w: %w", ErrWriteFailed, err)
	}
	return cw.n, nil
}

// PNG 生成并返回 PNG 格式的二进制图像字节切片。
func (b *Builder) PNG() ([]byte, error) {
	var buf bytes.Buffer
	if _, err := b.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Base64DataURI 生成并返回可直接用于 HTML <img> 标签 src 属性的 Data URI 格式字符串。
func (b *Builder) Base64DataURI() (string, error) {
	raw, err := b.PNG()
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw), nil
}

func (b *Builder) buildWriterOptions(dimension int) []standard.ImageOption {
	cellWidth := resolveCellWidth(b.cellWidth, b.sizeHint, b.border, dimension)
	opts := []standard.ImageOption{
		standard.WithQRWidth(cellWidth),
		standard.WithBorderWidth(b.border),
		standard.WithBuiltinImageEncoder(standard.PNG_FORMAT),
	}

	if b.circleShape {
		opts = append(opts, standard.WithCircleShape())
	}
	if b.bgTransparent {
		opts = append(opts, standard.WithBgTransparent())
	} else if b.bgColor != nil {
		opts = append(opts, standard.WithBgColor(b.bgColor))
	}
	if b.fgColor != nil {
		opts = append(opts, standard.WithFgColor(b.fgColor))
	}
	if b.logoImage != nil {
		opts = append(opts, standard.WithLogoImage(b.logoImage), standard.WithLogoSafeZone())
	}

	return opts
}

func resolveCellWidth(cellWidth uint8, sizeHint, border, dimension int) uint8 {
	if cellWidth > 0 {
		return cellWidth
	}
	if sizeHint <= 0 || dimension <= 0 {
		return 8
	}
	totalCells := dimension + border*2
	return uint8(min(255, max(1, sizeHint/totalCells)))
}

func toQRErrorCorrection(level ErrorCorrectionLevel) qrcode.EncodeOption {
	switch level {
	case ErrorCorrectionLow:
		return qrcode.WithErrorCorrectionLevel(qrcode.ErrorCorrectionLow)
	case ErrorCorrectionQuart:
		return qrcode.WithErrorCorrectionLevel(qrcode.ErrorCorrectionQuart)
	case ErrorCorrectionHighest:
		return qrcode.WithErrorCorrectionLevel(qrcode.ErrorCorrectionHighest)
	case ErrorCorrectionMedium, ErrorCorrectionDefault:
		fallthrough
	default:
		return qrcode.WithErrorCorrectionLevel(qrcode.ErrorCorrectionMedium)
	}
}

type countingWriteCloser struct {
	w io.Writer
	n int64
}

func (cw *countingWriteCloser) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	return n, err
}

func (*countingWriteCloser) Close() error {
	return nil
}

func applyOptions(content string, opts ...Option) *Builder {
	b := New(content)
	for _, opt := range opts {
		if opt != nil {
			opt(b)
		}
	}
	return b
}

// GeneratePNG 快捷函数：根据指定配置直接将文本转为 PNG 二维码字节流。
func GeneratePNG(content string, opts ...Option) ([]byte, error) {
	return applyOptions(content, opts...).PNG()
}

// GenerateBase64 快捷函数：根据指定配置直接将文本转为 Base64 Data URI。
func GenerateBase64(content string, opts ...Option) (string, error) {
	return applyOptions(content, opts...).Base64DataURI()
}
