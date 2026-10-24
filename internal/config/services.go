package config

// GeoIPConfig 描述基于 ip2region 的离线 IP 归属地库配置（对齐 Laravel config/services.php）。
type GeoIPConfig struct {
	Enabled bool   `mapstructure:"geoip_enabled"`
	DBPath  string `mapstructure:"geoip_db_path"`
}

// ExcelConfig 描述 Excel/CSV 导出导入管线临时存储与缓冲配置（对齐 Laravel config/services.php）。
type ExcelConfig struct {
	TempPath string `mapstructure:"excel_temp_path"`
}

// PDFConfig 描述 PDF 生成服务配置（对齐 Laravel config/services.php）。
type PDFConfig struct {
	FontPath   string  `mapstructure:"pdf_font_path"`
	FontFamily string  `mapstructure:"pdf_font_family"`
	PageSize   string  `mapstructure:"pdf_page_size"`
	Margin     float64 `mapstructure:"pdf_margin"`
}

// validateServices 检查第三方集成与扩展服务配置。
func (c *Config) validateServices() error {
	return nil
}
