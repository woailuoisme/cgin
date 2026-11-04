package media

import (
	"context"
	"strings"

	"github.com/samber/lo"

	"cgin/pkg/errs"
)

// Preset 描述不可变的图像衍生处理规格值对象。
type Preset struct {
	Name         string
	Width        int
	Height       int
	ResizingType ResizingType
	Gravity      GravityType
	Quality      int
	Format       Format
}

// Options 将规格值对象解包为 imgproxy 处理函数选项序列。
func (p Preset) Options() []ProcessOption {
	var opts []ProcessOption
	if p.Width > 0 || p.Height > 0 || p.ResizingType != "" {
		opts = append(opts, WithResize(p.Width, p.Height))
		if p.ResizingType != "" {
			opts = append(opts, WithResizingType(p.ResizingType))
		}
	}
	if p.Gravity != "" {
		opts = append(opts, WithGravity(p.Gravity))
	}
	if p.Quality > 0 {
		opts = append(opts, WithQuality(p.Quality))
	}
	if p.Format != "" {
		opts = append(opts, WithFormat(p.Format))
	}
	return opts
}

// 标准领域图像规格预设值对象。
var (
	// PresetAvatarLarge 用户头像标准大图（256x256，焦点人脸居中裁剪，WebP）。
	PresetAvatarLarge = Preset{
		Name:         "avatar_large",
		Width:        256,
		Height:       256,
		ResizingType: ResizeFill,
		Gravity:      GravityFocusPoint,
		Format:       FormatWebP,
	}

	// PresetAvatarThumb 用户头像微缩图（64x64，焦点人脸居中裁剪，WebP）。
	PresetAvatarThumb = Preset{
		Name:         "avatar_thumb",
		Width:        64,
		Height:       64,
		ResizingType: ResizeFill,
		Gravity:      GravityFocusPoint,
		Format:       FormatWebP,
	}

	// PresetCover 封面横幅横图（1200x630，智能居中，WebP）。
	PresetCover = Preset{
		Name:         "cover",
		Width:        1200,
		Height:       630,
		ResizingType: ResizeFill,
		Gravity:      GravityCenter,
		Format:       FormatWebP,
	}
)

// ImageVariants 封装一套完整的标准化图像衍生访问地址。
type ImageVariants struct {
	// 存储端相对路径（Key）
	Path string `json:"path" example:"uploads/avatars/20261031/uuid.png"`
	// 签名后标准尺寸访问 URL
	URL string `json:"url" example:"https://img.example.com/sig1/rs:fill:256:256:1/g:fp:0:0/...@webp"`
	// 签名后微缩图访问 URL
	ThumbURL string `json:"thumb_url" example:"https://img.example.com/sig2/rs:fill:64:64:1/g:fp:0:0/...@webp"`
}

// BuildImageVariants 根据相对路径与指定的 Signer 构建标准化图片衍生变体集。
// 未提供 signer 时安全回退为直读路径；路径为空时安全返回 nil。
func BuildImageVariants(rawPath string, signer Signer) *ImageVariants {
	cleanPath := strings.TrimSpace(rawPath)
	if cleanPath == "" {
		return nil
	}
	if signer == nil {
		return &ImageVariants{
			Path:     cleanPath,
			URL:      cleanPath,
			ThumbURL: cleanPath,
		}
	}
	return signer.Variants(cleanPath)
}

// Signer 定义纯内存/HMAC 图像变体与防篡改签名契约（零外部状态，完全并发安全）。
type Signer interface {
	// Sign 生成指定规格的 Imgproxy 防篡改签名访问 URL。
	Sign(path string, preset ...Preset) string
	// SignWithOptions 根据自定义函数式选项构建防篡改访问 URL。
	SignWithOptions(path string, opts ...ProcessOption) (string, error)
	// Variants 为存储相对路径生成全套标准图片衍生变体集。
	Variants(path string) *ImageVariants
	// Inspect 纯内存解析图像基础元数据（宽高、格式、大小）。
	Inspect(input []byte) (*ImageMeta, error)
	// Driver 返回图像处理驱动标识（如 imgproxy）。
	Driver() string
	// Ping 探针检测图像处理服务连通性（满足 ready.Pinger 契约）。
	Ping(ctx context.Context) error
}

var _ Signer = (*imgproxyClient)(nil)

// Sign 生成指定规格的 Imgproxy 防篡改签名访问 URL。
func (c *imgproxyClient) Sign(path string, preset ...Preset) string {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return cleanPath
	}

	p := lo.FirstOr(preset, PresetAvatarLarge)
	u, err := c.URL(cleanPath, p.Options()...)
	if err != nil {
		return cleanPath
	}
	return u
}

// SignWithOptions 根据自定义函数式选项构建防篡改访问 URL。
func (c *imgproxyClient) SignWithOptions(path string, opts ...ProcessOption) (string, error) {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return "", errs.BadRequest("path cannot be empty")
	}
	return c.URL(cleanPath, opts...)
}

// Variants 为存储相对路径生成全套标准图片衍生变体集。
func (c *imgproxyClient) Variants(path string) *ImageVariants {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return nil
	}
	return &ImageVariants{
		Path:     cleanPath,
		URL:      c.Sign(cleanPath, PresetAvatarLarge),
		ThumbURL: c.Sign(cleanPath, PresetAvatarThumb),
	}
}
