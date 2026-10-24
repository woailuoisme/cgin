package search

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/meilisearch/meilisearch-go"
	"github.com/samber/lo"
	"github.com/samber/oops"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/resilience"
)

var (
	_ Client        = (*meilisearchClient)(nil)
	_ Index         = (*meilisearchIndex)(nil)
	_ TypedSearcher = (*meilisearchIndex)(nil)
)

var meiliHTTPTransport = &http.Transport{
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 50,
	IdleConnTimeout:     90 * time.Second,
}

type meilisearchClient struct {
	manager meilisearch.ServiceManager
	prefix  string
}

var _ Client = (*meilisearchClient)(nil)

// NewMeilisearchClient 从已有的 Meilisearch ServiceManager 创建客户端门面。
func NewMeilisearchClient(manager meilisearch.ServiceManager, prefix ...string) Client {
	return &meilisearchClient{
		manager: manager,
		prefix:  lo.FirstOrEmpty(prefix),
	}
}

// NewClientFromConfig 根据配置创建 Meilisearch 官方客户端适配门面。
func NewClientFromConfig(cfg Config) (Client, error) {
	host := strings.TrimSpace(cfg.Meilisearch.Host)
	apiKey := strings.TrimSpace(cfg.Meilisearch.APIKey)
	if !cfg.Enabled() || host == "" {
		return NewNoop(), nil
	}

	client := meilisearch.New(
		host,
		meilisearch.WithAPIKey(apiKey),
		meilisearch.WithCustomClient(resilience.NewResilientHTTPClient(resilience.ResilientTransportConfig{
			BaseTransport: meiliHTTPTransport,
			Timeout:       5 * time.Second,
		})),
	)

	return NewMeilisearchClient(client, cfg.Prefix), nil
}

func (c *meilisearchClient) Index(name string) Index {
	prefixed := c.prefix + name
	return &meilisearchIndex{
		name:         name,
		prefixedName: prefixed,
		manager:      c.manager.Index(prefixed),
	}
}

func (c *meilisearchClient) Enabled() bool {
	return true
}

func (c *meilisearchClient) Driver() string {
	return DriverMeilisearch
}

func (c *meilisearchClient) Ping(ctx context.Context) error {
	resp, err := c.manager.HealthWithContext(ctx)
	if err != nil {
		return oops.Code(string(errs.CodeInternal)).In("search.Meilisearch.Ping").Wrap(err)
	}
	if resp == nil || resp.Status != "available" {
		return oops.Code(string(errs.CodeInternal)).In("search.Meilisearch.Ping").Errorf("meilisearch status is not available")
	}
	return nil
}

func (c *meilisearchClient) DeleteIndex(ctx context.Context, name string) error {
	prefixed := c.prefix + name
	_, err := c.manager.DeleteIndexWithContext(ctx, prefixed)
	if err != nil {
		return oops.Code(string(errs.CodeInternal)).In("search.Meilisearch.DeleteIndex").Wrap(err)
	}
	return nil
}

func (c *meilisearchClient) Raw() any {
	return c.manager
}

type meilisearchIndex struct {
	name         string
	prefixedName string
	manager      meilisearch.IndexManager
}

func (i *meilisearchIndex) Name() string {
	return i.name
}

func (i *meilisearchIndex) PrefixedName() string {
	return i.prefixedName
}

func (i *meilisearchIndex) Raw() any {
	return i.manager
}

func (i *meilisearchIndex) wrapError(op string, err error) error {
	if err == nil {
		return nil
	}
	if meiliErr, ok := errors.AsType[*meilisearch.Error](err); ok {
		if meiliErr.MeilisearchApiError.Code == "index_not_found" || meiliErr.StatusCode == http.StatusNotFound {
			return oops.Code(string(errs.CodeNotFound)).In("search.Meilisearch." + op).Wrap(err)
		}
	}
	return oops.Code(string(errs.CodeInternal)).In("search.Meilisearch." + op).Wrap(err)
}

func (i *meilisearchIndex) AddDocuments(ctx context.Context, documents any, primaryKey ...string) error {
	var opts *meilisearch.DocumentOptions
	if pk := lo.FirstOrEmpty(primaryKey); pk != "" {
		opts = &meilisearch.DocumentOptions{PrimaryKey: &pk}
	}

	_, err := i.manager.AddDocumentsWithContext(ctx, documents, opts)
	if err != nil {
		return i.wrapError("AddDocuments", err)
	}
	return nil
}

func (i *meilisearchIndex) DeleteDocuments(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := i.manager.DeleteDocumentsWithContext(ctx, ids, nil)
	if err != nil {
		return i.wrapError("DeleteDocuments", err)
	}
	return nil
}

func (i *meilisearchIndex) DeleteAllDocuments(ctx context.Context) error {
	_, err := i.manager.DeleteAllDocumentsWithContext(ctx, nil)
	if err != nil {
		return i.wrapError("DeleteAllDocuments", err)
	}
	return nil
}

func (i *meilisearchIndex) Search(ctx context.Context, query string, opts ...SearchOption) (*SearchResult, error) {
	var hits []map[string]any
	baseRes, err := i.SearchDirect(ctx, query, &hits, opts...)
	if err != nil {
		return nil, err
	}
	if hits == nil {
		hits = make([]map[string]any, 0)
	}
	baseRes.Hits = hits
	return baseRes, nil
}

// SearchDirect 直接向目标指针对象解码检索结果，实现零中间 map 分配。
func (i *meilisearchIndex) SearchDirect(ctx context.Context, query string, target any, opts ...SearchOption) (*SearchResult, error) {
	req := &SearchRequest{}
	for _, opt := range opts {
		opt(req)
	}

	meiliReq := &meilisearch.SearchRequest{
		Limit:  req.Limit,
		Offset: req.Offset,
		Filter: req.Filter,
		Sort:   req.Sort,
	}

	resp, err := i.manager.SearchWithContext(ctx, query, meiliReq)
	if err != nil {
		return nil, i.wrapError("Search", err)
	}

	if target != nil {
		if err := resp.Hits.DecodeInto(target); err != nil {
			return nil, oops.Code(string(errs.CodeInternal)).In("search.Meilisearch.Search.DecodeHits").Wrap(err)
		}
	}

	total := cmp.Or(resp.EstimatedTotalHits, resp.TotalHits)

	return &SearchResult{
		EstimatedTotalHits: total,
		Limit:              resp.Limit,
		Offset:             resp.Offset,
		ProcessingTimeMs:   resp.ProcessingTimeMs,
	}, nil
}
