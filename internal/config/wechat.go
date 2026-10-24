package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// defaultMiniProgramKey 是单应用便捷配置展开后的应用标识。
const defaultMiniProgramKey = "default"

// WeChatConfig 定义微信开放平台（公众平台 / 小程序）的服务端能力配置。
type WeChatConfig struct {
	// ApplicationsJSON 以 JSON 对象描述多个微信小程序凭据。
	// 形如 {"user": {"app_id":"...", "app_secret":"..."}, "merchant": {"app_id":"...", "app_secret":"..."}}。
	ApplicationsJSON string `mapstructure:"wechat_miniprogram_apps_json"`
	// MiniProgram 单小程序便捷配置：未提供 ApplicationsJSON 时作为默认应用生效。
	MiniProgram WeChatMiniProgramConfig `mapstructure:",squash"`
}

// Declared 判断微信能力是否已配置（多应用 JSON 或单应用环境变量任一已声明即视为已配置）。
func (c *WeChatConfig) Declared() bool {
	return strings.TrimSpace(c.ApplicationsJSON) != "" || c.MiniProgram.Declared()
}

// Applications 解析并返回全部已声明的小程序配置映射。
func (c *WeChatConfig) Applications() (map[string]WeChatMiniProgramConfig, error) {
	apps := make(map[string]WeChatMiniProgramConfig)

	if jsonText := strings.TrimSpace(c.ApplicationsJSON); jsonText != "" {
		if err := json.Unmarshal([]byte(jsonText), &apps); err != nil {
			return nil, fmt.Errorf("config: failed to parse WECHAT_MINIPROGRAM_APPS_JSON: %w", err)
		}
	}

	// 单应用便捷配置：若未在 JSON 中定义 default，则由 MiniProgram 兜底注入
	if c.MiniProgram.Declared() {
		if _, exists := apps[defaultMiniProgramKey]; !exists {
			apps[defaultMiniProgramKey] = c.MiniProgram
		}
	}

	return apps, nil
}

// WeChatMiniProgramConfig 定义单个微信小程序服务端凭据。
type WeChatMiniProgramConfig struct {
	// AppID 小程序 AppID。
	AppID string `mapstructure:"wechat_miniprogram_app_id" json:"app_id"`
	// AppSecret 小程序 AppSecret。
	AppSecret string `mapstructure:"wechat_miniprogram_app_secret" json:"app_secret"`
}

// Declared 判断小程序凭据是否已被声明。
// 沿用「配置即启用」的原则：填写任一字段即视为已声明。
func (c *WeChatMiniProgramConfig) Declared() bool {
	return strings.TrimSpace(c.AppID) != "" || strings.TrimSpace(c.AppSecret) != ""
}

// WeChatMiniProgramConfigured 判断小程序能力是否已配置。
func WeChatMiniProgramConfigured(cfg *Config) bool {
	if cfg == nil {
		return false
	}

	return cfg.WeChat.Declared()
}

// validateWeChat 校验微信配置项的有效性。
func (c *Config) validateWeChat() error {
	if !c.WeChat.Declared() {
		return nil
	}

	apps, err := c.WeChat.Applications()
	if err != nil {
		return err
	}

	for name, app := range apps {
		if strings.TrimSpace(app.AppID) == "" {
			return fmt.Errorf("config: wechat miniprogram %q AppID cannot be empty", name)
		}
		if strings.TrimSpace(app.AppSecret) == "" {
			return fmt.Errorf("config: wechat miniprogram %q AppSecret cannot be empty", name)
		}
	}

	return nil
}
