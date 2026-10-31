// Package media 提供媒体文件上传、验证、处理与 Imgproxy 防篡改签名服务。
package media

import (
	"strings"

	"github.com/samber/lo"
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

var defaultEngine Engine

// SetDefaultEngine 注册全局默认媒体引擎，供变体投影与防篡改签名安全使用。
func SetDefaultEngine(e Engine) {
	defaultEngine = e
}

// DefaultEngine 获取全局默认媒体引擎。
func DefaultEngine() Engine {
	return defaultEngine
}

// ImageVariants 封装一套完整的标准化图像衍生访问地址。
type ImageVariants struct {
	// 存储端相对路径（Key）
	Path string `json:"path" example:"uploads/avatars/20261031/uuid.png"`
	// 签名后标准尺寸访问 URL
	URL string `json:"url" example:"https://img.example.com/sig1/rs:fill:256:256:1/g:fp:0:0/...@webp"`
	// 签名后微缩图访问 URL
	ThumbURL string `json:"thumb_url" example:"https://img.example.com/sig2/rs:fill:64:64:1/g:fp:0:0/...@webp"`
}

// BuildImageVariants 根据相对路径与可选的媒体引擎，构建标准化图片衍生变体集。
// 未显式指定 engine 时自动使用全局 DefaultEngine()；路径为空时安全返回 nil。
func BuildImageVariants(rawPath string, engine ...Engine) *ImageVariants {
	cleanPath := strings.TrimSpace(rawPath)
	if cleanPath == "" {
		return nil
	}
	e := lo.FirstOr(engine, defaultEngine)
	if e == nil {
		return &ImageVariants{
			Path:     cleanPath,
			URL:      cleanPath,
			ThumbURL: cleanPath,
		}
	}
	return e.Variants(cleanPath)
}
