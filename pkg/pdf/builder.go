package pdf

import (
	"cmp"
	"context"
	"path/filepath"

	"github.com/johnfercher/maroto/v2/pkg/components/code"
	"github.com/johnfercher/maroto/v2/pkg/components/col"
	"github.com/johnfercher/maroto/v2/pkg/components/image"
	"github.com/johnfercher/maroto/v2/pkg/components/line"
	"github.com/johnfercher/maroto/v2/pkg/components/row"
	"github.com/johnfercher/maroto/v2/pkg/components/signature"
	"github.com/johnfercher/maroto/v2/pkg/components/text"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/extension"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/consts/linestyle"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/props"
	"github.com/samber/lo"
)

// Builder 封装 Maroto v2 提供企业级排版流式构建器。
type Builder struct {
	m          core.Maroto
	fontFamily string
}

// NewBuilder 实例化增强型 PDF 布局构建器。
func NewBuilder(m core.Maroto, fontFamily string) *Builder {
	return &Builder{
		m:          m,
		fontFamily: fontFamily,
	}
}

// Raw 暴露底层的 core.Maroto 实例，便于直接调用低阶原生 API。
func (b *Builder) Raw() core.Maroto {
	return b.m
}

// FontFamily 返回当前配置的默认字体族名。
func (b *Builder) FontFamily() string {
	return b.fontFamily
}

// AddComponent 编排渲染可复用的业务视觉组件。
func (b *Builder) AddComponent(ctx context.Context, comp Component) error {
	if comp == nil {
		return nil
	}
	return comp.Render(ctx, b)
}

// AddSpace 添加指定高度的空白留白行。
func (b *Builder) AddSpace(height float64) *Builder {
	b.m.AddRow(resolveRowHeight(height, 3.0))
	return b
}

// AddTitle 添加文档一级大标题。
func (b *Builder) AddTitle(title string, aligns ...align.Type) *Builder {
	b.m.AddRow(10.0,
		text.NewCol(12, title, props.Text{
			Size:   FontSizeTitle,
			Style:  fontstyle.Bold,
			Family: b.fontFamily,
			Color:  &ColorPrimary,
			Align:  resolveAlign(aligns),
		}),
	)
	return b
}

// AddSubtitle 添加二级章节副标题。
func (b *Builder) AddSubtitle(subtitle string, aligns ...align.Type) *Builder {
	b.m.AddRow(8.0,
		text.NewCol(12, subtitle, props.Text{
			Size:   FontSizeSubtitle,
			Style:  fontstyle.Bold,
			Family: b.fontFamily,
			Color:  &ColorTextSecondary,
			Align:  resolveAlign(aligns),
		}),
	)
	return b
}

// AddHeading 添加栏目分组小标题。
func (b *Builder) AddHeading(heading string, aligns ...align.Type) *Builder {
	b.m.AddRow(7.0,
		text.NewCol(12, heading, props.Text{
			Size:   FontSizeHeading,
			Style:  fontstyle.Bold,
			Family: b.fontFamily,
			Color:  &ColorTextPrimary,
			Align:  resolveAlign(aligns),
		}),
	)
	return b
}

// AddParagraph 添加标准正文段落。
func (b *Builder) AddParagraph(content string, aligns ...align.Type) *Builder {
	b.m.AddAutoRow(
		text.NewCol(12, content, props.Text{
			Size:   FontSizeBody,
			Style:  fontstyle.Normal,
			Family: b.fontFamily,
			Color:  &ColorTextPrimary,
			Align:  resolveAlign(aligns),
		}),
	)
	return b
}

// AddDivider 绘制一条水平分割线。
func (b *Builder) AddDivider(height ...float64) *Builder {
	var h float64
	if len(height) > 0 {
		h = height[0]
	}

	b.m.AddRow(resolveRowHeight(h, 4.0),
		line.NewCol(12, props.Line{
			Color:         &ColorBorder,
			Thickness:     0.5,
			Style:         linestyle.Solid,
			SizePercent:   100,
			OffsetPercent: 50,
		}),
	)
	return b
}

// KeyValuePair 描述元数据键值对。
type KeyValuePair struct {
	Key   string
	Value string
}

// AddKeyValueGrid 以网格形式排布键值对信息（如订单号、开票日期、客户名称等）。
func (b *Builder) AddKeyValueGrid(pairs []KeyValuePair, colsPerRow int, rowHeight ...float64) *Builder {
	if len(pairs) == 0 {
		return b
	}
	if colsPerRow <= 0 || colsPerRow > 4 {
		colsPerRow = 2
	}
	var h float64
	if len(rowHeight) > 0 {
		h = rowHeight[0]
	}

	colSpan := 12 / colsPerRow
	chunks := lo.Chunk(pairs, colsPerRow)

	for _, chunk := range chunks {
		var cols []core.Col
		for _, pair := range chunk {
			cols = append(cols, text.NewCol(colSpan, pair.Key+": "+pair.Value, props.Text{
				Size:   FontSizeBody,
				Family: b.fontFamily,
				Color:  &ColorTextPrimary,
				Align:  align.Left,
			}))
		}
		b.m.AddRow(resolveRowHeight(h, 7.0), cols...)
	}

	return b
}

// TableColumn 表格单列配置。
type TableColumn struct {
	Header string
	Size   int
	Align  align.Type
}

// TableConfig 表格全局配置项。
type TableConfig struct {
	Columns      []TableColumn
	Rows         [][]string
	HeaderHeight float64
	RowHeight    float64
	Zebra        bool
	HeaderBg     *props.Color
}

// AddTable 快速生成企业级标准化斑马纹数据表格。
func (b *Builder) AddTable(cfg TableConfig) *Builder {
	if len(cfg.Columns) == 0 {
		return b
	}

	// 1. 计算列宽（若未分配或总和不合法则均分）
	cols := make([]TableColumn, len(cfg.Columns))
	copy(cols, cfg.Columns)
	totalSize := lo.SumBy(cols, func(c TableColumn) int { return c.Size })
	if totalSize != 12 {
		span := 12 / len(cols)
		for i := range cols {
			cols[i].Size = span
		}
		// 余数补全给最后一列
		rem := 12 - (span * len(cols))
		if rem > 0 {
			cols[len(cols)-1].Size += rem
		}
	}

	headerH := resolveRowHeight(cfg.HeaderHeight, 8.0)
	rowH := resolveRowHeight(cfg.RowHeight, 7.0)
	headerBg := cmp.Or(cfg.HeaderBg, &ColorBgHeader)

	// 2. 渲染表头
	var headerCols []core.Col
	for _, c := range cols {
		headerCols = append(headerCols, text.NewCol(c.Size, c.Header, props.Text{
			Size:   FontSizeHeading,
			Style:  fontstyle.Bold,
			Family: b.fontFamily,
			Color:  &ColorTextPrimary,
			Align:  cmp.Or(c.Align, align.Left),
		}))
	}

	headerRow := row.New(headerH).Add(headerCols...).WithStyle(&props.Cell{
		BackgroundColor: headerBg,
	})
	b.m.AddRows(headerRow)

	// 3. 渲染数据行（支持斑马纹）
	for i, r := range cfg.Rows {
		var rowCols []core.Col
		for colIdx, cellValue := range r {
			if colIdx >= len(cols) {
				break
			}
			colCfg := cols[colIdx]
			rowCols = append(rowCols, text.NewCol(colCfg.Size, cellValue, props.Text{
				Size:   FontSizeBody,
				Style:  fontstyle.Normal,
				Family: b.fontFamily,
				Color:  &ColorTextPrimary,
				Align:  cmp.Or(colCfg.Align, align.Left),
			}))
		}

		dataRow := row.New(rowH).Add(rowCols...)
		if cfg.Zebra && i%2 == 1 {
			dataRow.WithStyle(&props.Cell{
				BackgroundColor: &ColorBgZebra,
			})
		}
		b.m.AddRows(dataRow)
	}

	return b
}

// AddQRCode 添加二维码展示块。
func (b *Builder) AddQRCode(content string, size int, height float64, aligns ...align.Type) *Builder {
	b.m.AddRow(resolveRowHeight(height, 30.0),
		code.NewQrCol(resolveColSize(size, 4), content, props.Rect{
			Center:  true,
			Percent: 90,
		}),
	)
	return b
}

// AddBarcode 添加条形码展示块。
func (b *Builder) AddBarcode(content string, size int, height float64) *Builder {
	b.m.AddRow(resolveRowHeight(height, 20.0),
		code.NewBarCol(resolveColSize(size, 6), content, props.Barcode{
			Center:  true,
			Percent: 90,
		}),
	)
	return b
}

// AddImageFromFile 从本地文件加载并添加图片（自动净化文件路径）。
func (b *Builder) AddImageFromFile(filePath string, size int, height float64) *Builder {
	cleanPath := filepath.Clean(filePath)
	b.m.AddRow(resolveRowHeight(height, 20.0),
		image.NewFromFileCol(resolveColSize(size, 4), cleanPath, props.Rect{
			Center:  true,
			Percent: 95,
		}),
	)
	return b
}

// AddImageFromBytes 从内存字节流添加图片。
func (b *Builder) AddImageFromBytes(data []byte, ext extension.Type, size int, height float64) *Builder {
	b.m.AddRow(resolveRowHeight(height, 20.0),
		image.NewFromBytesCol(resolveColSize(size, 4), data, ext, props.Rect{
			Center:  true,
			Percent: 95,
		}),
	)
	return b
}

// AddSignature 添加落款签名/盖章区域。
func (b *Builder) AddSignature(label string, size int, height float64) *Builder {
	actualSize := resolveColSize(size, 4)
	b.m.AddRow(resolveRowHeight(height, 18.0),
		col.New(12-actualSize),
		signature.NewCol(actualSize, label, props.Signature{
			FontFamily: b.fontFamily,
			FontSize:   FontSizeCaption,
			FontColor:  &ColorTextSecondary,
			LineColor:  &ColorBorder,
		}),
	)
	return b
}

func resolveAlign(aligns []align.Type) align.Type {
	if len(aligns) > 0 {
		return aligns[0]
	}
	return align.Left
}

func resolveColSize(size, defaultVal int) int {
	if size <= 0 || size > 12 {
		return defaultVal
	}
	return size
}

func resolveRowHeight(height, defaultVal float64) float64 {
	if height <= 0 {
		return defaultVal
	}
	return height
}
