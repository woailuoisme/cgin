// Package search 提供基于 Meilisearch 的全文字典检索与安全降级基础设施（Laravel Scout 风格）。
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrDisabled 表示全文搜索未启用或未配置服务地址。
var ErrDisabled = errors.New("search engine is disabled or not configured")

// New 根据配置智能装配具体的底层驱动客户端（支持 meilisearch、noop 及注册的第三方驱动）。
func New(cfg Config) (Client, error) {
	return createDriver(cfg)
}

// SearchOption 定义搜索查询的 Functional Option 函数签名。
type SearchOption func(*SearchRequest)

// SearchRequest 描述通用搜索查询参数。
type SearchRequest struct {
	Limit  int64
	Offset int64
	Filter string
	Sort   []string
}

// WithLimit 设置返回结果数量限制。
func WithLimit(limit int64) SearchOption {
	return func(r *SearchRequest) {
		r.Limit = limit
	}
}

// WithOffset 设置返回结果偏移量。
func WithOffset(offset int64) SearchOption {
	return func(r *SearchRequest) {
		r.Offset = offset
	}
}

// WithFilter 设置过滤表达式。
func WithFilter(filter string) SearchOption {
	return func(r *SearchRequest) {
		r.Filter = filter
	}
}

// WithSort 设置排序规则。
func WithSort(sort ...string) SearchOption {
	return func(r *SearchRequest) {
		r.Sort = sort
	}
}

// SearchResult 封装结构化检索结果。
type SearchResult struct {
	Hits               []map[string]any `json:"hits"`
	EstimatedTotalHits int64            `json:"estimatedTotalHits"`
	Limit              int64            `json:"limit"`
	Offset             int64            `json:"offset"`
	ProcessingTimeMs   int64            `json:"processingTimeMs"`
}

// TypedSearchResult 封装泛型强类型检索结果，消除中间 map[string]any 二次转换。
type TypedSearchResult[T any] struct {
	Hits               []T   `json:"hits"`
	EstimatedTotalHits int64 `json:"estimatedTotalHits"`
	Limit              int64 `json:"limit"`
	Offset             int64 `json:"offset"`
	ProcessingTimeMs   int64 `json:"processingTimeMs"`
}

// TypedSearcher 允许驱动原生支持直接解码为目标类型切片，省去中间反射反序列化。
type TypedSearcher interface {
	SearchDirect(ctx context.Context, query string, target any, opts ...SearchOption) (*SearchResult, error)
}

// SearchTyped 执行全文检索并直接解码为强类型结果切片，消除中间 map[string]any 二次转换的开销。
func SearchTyped[T any](ctx context.Context, idx Index, query string, opts ...SearchOption) (*TypedSearchResult[T], error) {
	if idx == nil {
		return &TypedSearchResult[T]{Hits: []T{}}, nil
	}

	// 1. 若底层索引驱动原生支持直接解码（如 meilisearchIndex），直接借力 SDK 的 JSON 流解码
	var typedHits []T
	if ts, ok := idx.(TypedSearcher); ok {
		baseRes, err := ts.SearchDirect(ctx, query, &typedHits, opts...)
		if err != nil {
			return nil, err
		}
		if typedHits == nil {
			typedHits = make([]T, 0)
		}
		return &TypedSearchResult[T]{
			Hits:               typedHits,
			EstimatedTotalHits: baseRes.EstimatedTotalHits,
			Limit:              baseRes.Limit,
			Offset:             baseRes.Offset,
			ProcessingTimeMs:   baseRes.ProcessingTimeMs,
		}, nil
	}

	// 2. 兜底回退：执行通用 Search 并通过 JSON 转换
	res, err := idx.Search(ctx, query, opts...)
	if err != nil {
		return nil, err
	}

	typed, err := DecodeHits[T](res)
	if err != nil {
		return nil, err
	}

	return &TypedSearchResult[T]{
		Hits:               typed,
		EstimatedTotalHits: res.EstimatedTotalHits,
		Limit:              res.Limit,
		Offset:             res.Offset,
		ProcessingTimeMs:   res.ProcessingTimeMs,
	}, nil
}

// DecodeHits 将通用 SearchResult 中的 map 切片解码为强类型切片。
func DecodeHits[T any](res *SearchResult) ([]T, error) {
	if res == nil || len(res.Hits) == 0 {
		return make([]T, 0), nil
	}

	data, err := json.Marshal(res.Hits)
	if err != nil {
		return nil, fmt.Errorf("search: failed to marshal hits: %w", err)
	}

	var typed []T
	if err := json.Unmarshal(data, &typed); err != nil {
		return nil, fmt.Errorf("search: failed to unmarshal hits to target type: %w", err)
	}
	return typed, nil
}

// Index 定义单个搜索索引的核心操作契约。
type Index interface {
	// Name 返回基础索引名称。
	Name() string
	// PrefixedName 返回实际在底层引擎中应用了全局前缀的物理索引名称。
	PrefixedName() string
	// AddDocuments 批量添加或替换索引文档（可指定主键字段名，如 "id"）。
	AddDocuments(ctx context.Context, documents any, primaryKey ...string) error
	// DeleteDocuments 依据标识符切片批量删除索引文档。
	DeleteDocuments(ctx context.Context, ids []string) error
	// DeleteAllDocuments 清空该索引下的所有文档。
	DeleteAllDocuments(ctx context.Context) error
	// Search 执行全文检索。
	Search(ctx context.Context, query string, opts ...SearchOption) (*SearchResult, error)
	// Raw 返回底层原生索引管理器实例（若当前为 Noop 降级驱动则返回 nil）。
	Raw() any
}

// Client 定义全文搜索引擎中台客户端契约。
type Client interface {
	// Index 获取或创建具名索引门面（自动应用全局 SCOUT_PREFIX 索引前缀）。
	Index(name string) Index
	// DeleteIndex 依据名称删除指定索引。
	DeleteIndex(ctx context.Context, name string) error
	// Enabled 指示当前搜索引擎是否已启用且配置有效。
	Enabled() bool
	// Driver 返回当前激活的搜索引擎驱动名称（如 meilisearch、algolia、noop）。
	Driver() string
	// Ping 执行连接可用性健康探测。
	Ping(ctx context.Context) error
	// Raw 返回底层原生 SDK 客户端实例（若当前为 Noop 降级驱动则返回 nil）。
	Raw() any
}
