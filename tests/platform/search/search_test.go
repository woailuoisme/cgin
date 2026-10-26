// Package search_test 验证全文检索基础设施、驱动工厂与安全降级契约。
package search_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/pkg/search"
)

func TestNoopClientContract(t *testing.T) {
	ctx := t.Context()
	client := search.NewNoop()

	assert.False(t, client.Enabled())
	assert.Equal(t, "noop", client.Driver())
	assert.NoError(t, client.Ping(ctx))
	assert.NoError(t, client.DeleteIndex(ctx, "users"))
	assert.Nil(t, client.Raw())

	idx := client.Index("users")
	require.NotNil(t, idx)
	assert.Equal(t, "users", idx.Name())
	assert.Equal(t, "users", idx.PrefixedName())
	assert.Nil(t, idx.Raw())
	assert.NoError(t, idx.AddDocuments(ctx, []map[string]any{{"id": "1"}}))
	assert.NoError(t, idx.DeleteDocuments(ctx, []string{"1"}))
	assert.NoError(t, idx.DeleteAllDocuments(ctx))

	res, err := idx.Search(ctx, "alex")
	require.NoError(t, err)
	assert.Empty(t, res.Hits)
	assert.Zero(t, res.EstimatedTotalHits)
}

func TestSearchFactory(t *testing.T) {
	// 1. 默认无配置或 noop 驱动返回 NoopClient
	client, err := search.New(config.SearchConfig{
		Driver: "noop",
	})
	require.NoError(t, err)
	assert.Equal(t, "noop", client.Driver())
	assert.False(t, client.Enabled())

	// 2. 声明 meilisearch 但未配置 Host 时回退至 NoopClient
	client, err = search.New(config.SearchConfig{
		Driver: "meilisearch",
		Meilisearch: config.MeilisearchConfig{
			Host: "",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "noop", client.Driver())

	// 3. 配置有效的 Meilisearch 驱动
	client, err = search.New(config.SearchConfig{
		Driver: "meilisearch",
		Prefix: "test_",
		Meilisearch: config.MeilisearchConfig{
			Host:   "http://127.0.0.1:7700",
			APIKey: "masterKey",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "meilisearch", client.Driver())
	assert.True(t, client.Enabled())
	assert.NotNil(t, client.Raw())

	// 检查前缀拼接
	idx := client.Index("users")
	require.NotNil(t, idx)
	assert.Equal(t, "users", idx.Name())
	assert.Equal(t, "test_users", idx.PrefixedName())
	assert.NotNil(t, idx.Raw())
}

func TestSearchOptions(t *testing.T) {
	req := &search.SearchRequest{}
	opts := []search.SearchOption{
		search.WithLimit(50),
		search.WithOffset(10),
		search.WithFilter("status = active"),
		search.WithSort("created_at:desc", "id:asc"),
	}

	for _, opt := range opts {
		opt(req)
	}

	assert.Equal(t, int64(50), req.Limit)
	assert.Equal(t, int64(10), req.Offset)
	assert.Equal(t, "status = active", req.Filter)
	assert.Equal(t, []string{"created_at:desc", "id:asc"}, req.Sort)
}

func TestModelCatalogLookup(t *testing.T) {
	dummyImporter := func(_ context.Context, _ int, _ search.ChunkHandler) (int64, error) {
		return 0, nil
	}

	catalog := search.NewCatalog(search.ModelDescriptor{
		Name:      "article",
		IndexName: "articles",
		Importer:  dummyImporter,
	})

	desc, ok := catalog.Lookup("article")
	require.True(t, ok)
	assert.Equal(t, "article", desc.Name)
	assert.Equal(t, "articles", desc.IndexName)

	descByIndex, ok := catalog.Lookup("articles")
	require.True(t, ok)
	assert.Equal(t, "article", descByIndex.Name)

	_, ok = catalog.Lookup("unknown")
	assert.False(t, ok)

	assert.Len(t, catalog.All(), 1)
}
