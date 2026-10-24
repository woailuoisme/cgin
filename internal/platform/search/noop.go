package search

import (
	"context"
)

type noopClient struct{}

var (
	_ Client        = (*noopClient)(nil)
	_ Index         = (*noopIndex)(nil)
	_ TypedSearcher = (*noopIndex)(nil)
)

// NewNoop 创建安全的静默降级搜索客户端。
// 写操作直接返回成功，搜索操作返回空命中结果，杜绝未启用搜索时的应用崩溃。
func NewNoop() Client {
	return &noopClient{}
}

func (n *noopClient) Index(name string) Index {
	return &noopIndex{name: name}
}

func (n *noopClient) Enabled() bool {
	return false
}

func (n *noopClient) Driver() string {
	return DriverNoop
}

func (n *noopClient) Ping(_ context.Context) error {
	return nil
}

func (n *noopClient) DeleteIndex(_ context.Context, _ string) error {
	return nil
}

func (n *noopClient) Raw() any {
	return nil
}

type noopIndex struct {
	name string
}

func (n *noopIndex) Name() string {
	return n.name
}

func (n *noopIndex) PrefixedName() string {
	return n.name
}

func (n *noopIndex) AddDocuments(_ context.Context, _ any, _ ...string) error {
	return nil
}

func (n *noopIndex) DeleteDocuments(_ context.Context, _ []string) error {
	return nil
}

func (n *noopIndex) DeleteAllDocuments(_ context.Context) error {
	return nil
}

func (n *noopIndex) Search(_ context.Context, _ string, _ ...SearchOption) (*SearchResult, error) {
	return &SearchResult{
		Hits:               []map[string]any{},
		EstimatedTotalHits: 0,
		Limit:              0,
		Offset:             0,
		ProcessingTimeMs:   0,
	}, nil
}

func (n *noopIndex) SearchDirect(_ context.Context, _ string, _ any, _ ...SearchOption) (*SearchResult, error) {
	return &SearchResult{
		Hits:               []map[string]any{},
		EstimatedTotalHits: 0,
		Limit:              0,
		Offset:             0,
		ProcessingTimeMs:   0,
	}, nil
}

func (n *noopIndex) Raw() any {
	return nil
}
