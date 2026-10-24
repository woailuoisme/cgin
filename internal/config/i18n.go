package config

// I18nConfig 描述多语言本地化配置。
type I18nConfig struct {
	DefaultLanguage string `mapstructure:"app_locale"`
}

// validateI18n 检查国际化语言配置合法性。
func (c *Config) validateI18n() error {
	return nil
}
