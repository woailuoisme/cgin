package pdf

import (
	"bytes"
	"cmp"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/johnfercher/maroto/v2/pkg/consts/fontfamily"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/core/entity"
)

// FontDefinition 描述自定义字体注册元数据，实现 entity.CustomFont 契约。
type FontDefinition struct {
	Family string
	Style  fontstyle.Type
	File   string
	Bytes  []byte
}

var _ entity.CustomFont = (*FontDefinition)(nil)

func (f *FontDefinition) GetFamily() string        { return f.Family }
func (f *FontDefinition) GetStyle() fontstyle.Type { return f.Style }
func (f *FontDefinition) GetFile() string          { return f.File }
func (f *FontDefinition) GetBytes() []byte         { return bytes.Clone(f.Bytes) }

// NewCustomFontFromFile 基于本地 TTF/OTF 文件路径构造 Maroto 字体对象。
func NewCustomFontFromFile(family string, style fontstyle.Type, filePath string) entity.CustomFont {
	return &FontDefinition{
		Family: family,
		Style:  style,
		File:   filePath,
	}
}

// NewCustomFontFromBytes 基于内存字节流构造 Maroto 字体对象。
// 采用防御性拷贝隔离输入切片，遵循 Uber Go 边界规范。
func NewCustomFontFromBytes(family string, style fontstyle.Type, data []byte) entity.CustomFont {
	return &FontDefinition{
		Family: family,
		Style:  style,
		Bytes:  bytes.Clone(data),
	}
}

// ResolveFontConfig 解析并加载字体配置，支持本地文件、内存字节与自动回退。
func ResolveFontConfig(fontPath, fontFamily string, log *slog.Logger) ([]entity.CustomFont, string) {
	fontFamily = cmp.Or(fontFamily, "SourceHanSans")

	if fontPath == "" {
		return nil, fontfamily.Helvetica
	}

	cleanFontPath := filepath.Clean(fontPath)
	info, err := os.Stat(cleanFontPath)

	if err != nil || info.IsDir() {
		return nil, fontfamily.Helvetica
	}

	if log != nil {
		log.Info("loaded custom PDF font successfully",
			slog.String("family", fontFamily),
			slog.String("path", fontPath),
		)
	}

	// 注册 Regular / Bold 等基础变体（若单文件则映射到主要样式）
	fonts := []entity.CustomFont{
		NewCustomFontFromFile(fontFamily, fontstyle.Normal, cleanFontPath),
		NewCustomFontFromFile(fontFamily, fontstyle.Bold, cleanFontPath),
		NewCustomFontFromFile(fontFamily, fontstyle.Italic, cleanFontPath),
		NewCustomFontFromFile(fontFamily, fontstyle.BoldItalic, cleanFontPath),
	}
	return fonts, fontFamily
}
