package search

import "bgin/internal/config"

// Config 为全局搜索配置的类型别名，便于上层直接通过 search.Config 进行引用。
type Config = config.SearchConfig

// MeilisearchConfig 为 Meilisearch 专有驱动配置别名。
type MeilisearchConfig = config.MeilisearchConfig
