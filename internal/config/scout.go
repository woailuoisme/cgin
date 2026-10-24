package config

import (
	"fmt"
	"strings"
)

// SearchConfig 描述全文检索驱动与连接参数（对齐 Laravel config/scout.php）。
type SearchConfig struct {
	Driver      string `env:"SCOUT_DRIVER" envDefault:"noop"`
	Prefix      string `env:"SCOUT_PREFIX"`
	Meilisearch MeilisearchConfig
}

// MeilisearchConfig 描述 Meilisearch 连接与鉴权凭据。
type MeilisearchConfig struct {
	Host   string `env:"MEILISEARCH_HOST" envDefault:"http://127.0.0.1:7700"`
	APIKey string `env:"MEILISEARCH_KEY"`
}

// Enabled 报告是否启用了实际的搜索驱动服务。
func (c *SearchConfig) Enabled() bool {
	return c.Driver == "meilisearch" && strings.TrimSpace(c.Meilisearch.Host) != ""
}

// validateSearch 检查全文检索驱动配置。
func (c *Config) validateSearch() error {
	switch c.Search.Driver {
	case "", "noop", "meilisearch":
		return nil
	default:
		return fmt.Errorf("config: SCOUT_DRIVER=%q is not supported, options: noop / meilisearch", c.Search.Driver)
	}
}
