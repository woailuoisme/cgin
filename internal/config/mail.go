package config

import (
	"errors"
	"strings"
)

// MailConfig 描述邮件发信服务配置，支持 mailpit、resend、log、null 与 memory 驱动。
type MailConfig struct {
	// Enabled 控制是否启用真实邮件外发 (默认 true；设为 false 时无论配置何种驱动均自动旁路降级为 log 发信)。
	Enabled *bool `mapstructure:"mail_enabled"`
	// Driver 邮件发送驱动 (mailpit: 本地开发与测试, resend: 现代化云端发信, log: 控制台结构化日志, null: 静默丢弃, memory: 内存测试)
	Driver string `mapstructure:"mail_driver"`
	// FromAddress 发件人邮箱地址
	FromAddress string `mapstructure:"mail_from_address"`
	// FromName 发件人展示名称
	FromName string `mapstructure:"mail_from_name"`

	// Mailpit 驱动配置 (本地开发)
	Mailpit MailpitConfig `mapstructure:",squash"`
	// Resend 驱动配置 (云端发信)
	Resend ResendConfig `mapstructure:",squash"`
}

// MailpitConfig 描述 Mailpit 驱动连接参数。
type MailpitConfig struct {
	Host string `mapstructure:"mailpit_host"`
	Port int    `mapstructure:"mailpit_port"`
}

// ResendConfig 描述 Resend API 驱动配置。
type ResendConfig struct {
	APIKey  string `mapstructure:"resend_api_key"`
	BaseURL string `mapstructure:"resend_base_url"`
}

// IsEnabled 报告是否启用真实邮件发送服务。
// 当显式配置了 *Enabled 时遵循该值；未设置（如代码零值初始化）时默认视为启用 (true)。
func (m MailConfig) IsEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}

// Validate 检查邮件配置合法性。
func (m *MailConfig) Validate() error {
	if !m.IsEnabled() {
		return nil
	}

	driver := strings.ToLower(strings.TrimSpace(m.Driver))
	switch driver {
	case "mailpit", "":
		if m.Mailpit.Port <= 0 || m.Mailpit.Port > 65535 {
			return errors.New("mailpit port must be between 1 and 65535")
		}
	case "resend":
		if strings.TrimSpace(m.Resend.APIKey) == "" {
			return errors.New("resend api key cannot be empty when mail driver is resend")
		}
	case "log", "null", "memory":
		// 开箱即用无需网络凭据
	default:
		return errors.New("unsupported mail driver: " + m.Driver + " (supported: 'mailpit', 'resend', 'log', 'null', 'memory')")
	}

	return nil
}

// validateMail 检查邮件配置合法性。
func (c *Config) validateMail() error {
	return c.Mail.Validate()
}
