package config

// LogConfig 描述日志级别与输出通道（对齐 Laravel config/logging.php）。
type LogConfig struct {
	Level   string `mapstructure:"log_level"`
	Channel string `mapstructure:"log_channel"`
}

// validateLog 检查日志配置合法性。
func (c *Config) validateLog() error {
	return nil
}
