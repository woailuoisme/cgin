package search

import (
	"fmt"
	"strings"
)

// Driver 代表支持的搜索引擎驱动类型常量。
const (
	DriverMeilisearch = "meilisearch"
	DriverNoop        = "noop"
)

// createDriver 依据配置构造对应的搜索引擎客户端。
func createDriver(cfg Config) (Client, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Driver)) {
	case "", DriverNoop:
		return NewNoop(), nil
	case DriverMeilisearch:
		return NewClientFromConfig(cfg)
	default:
		return nil, fmt.Errorf("unsupported search driver: %s", cfg.Driver)
	}
}
