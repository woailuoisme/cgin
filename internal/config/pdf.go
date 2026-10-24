package config

// PDFConfig 描述 PDF 生成服务的字体与页面参数。
type PDFConfig struct {
	FontPath   string  `env:"PDF_FONT_PATH" envDefault:"./storage/fonts/SourceHanSans.ttf"`
	FontFamily string  `env:"PDF_FONT_FAMILY" envDefault:"SourceHanSans"`
	PageSize   string  `env:"PDF_PAGE_SIZE" envDefault:"A4"`
	Margin     float64 `env:"PDF_MARGIN" envDefault:"10.0"`
}
