// Package search_test 验证全文检索基础设施、驱动工厂与安全降级契约。
package search_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"bgin/internal/config"
	"bgin/internal/platform/search"
	"bgin/tests/testutil"
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

func TestModelDescriptorRegistry(t *testing.T) {
	dummyImporter := func(_ context.Context, _ bun.IDB, _ int, _ search.ChunkHandler) (int64, error) {
		return 0, nil
	}

	search.RegisterModel("article", "articles", dummyImporter)

	desc, ok := search.GetModel("article")
	require.True(t, ok)
	assert.Equal(t, "article", desc.Name)
	assert.Equal(t, "articles", desc.IndexName)

	descByIndex, ok := search.GetModel("articles")
	require.True(t, ok)
	assert.Equal(t, "article", descByIndex.Name)

	models := search.RegisteredModels()
	assert.NotEmpty(t, models)
}

func TestUserSearchableContract(t *testing.T) {
	username := "alex_dev"
	display := "Alex"
	u := testutil.User().
		WithID("usr_123").
		WithName("Alex Doe").
		WithEmail("alex@example.com").
		WithPhone("+1234567890").
		Build()
	u.Username = &username
	u.DisplayUsername = &display

	// 静态类型断言：*models.User 满足 search.Searchable 接口
	var _ search.Searchable = u

	assert.Equal(t, "users", u.SearchableAs())
	assert.Equal(t, "usr_123", u.SearchableKey())

	doc := u.ToSearchableArray()
	assert.Equal(t, "usr_123", doc["id"])
	assert.Equal(t, "Alex Doe", doc["name"])
	assert.Equal(t, "alex@example.com", doc["email"])
	assert.Equal(t, "user", doc["role"])
	assert.Equal(t, "active", doc["status"])
	assert.Equal(t, true, doc["email_verified"])
	assert.Equal(t, "alex_dev", doc["username"])
	assert.Equal(t, "Alex", doc["display_username"])
	assert.Equal(t, "+1234567890", doc["phone_number"])
	assert.Equal(t, u.CreatedAt.Unix(), doc["created_at"])
	assert.Equal(t, u.UpdatedAt.Unix(), doc["updated_at"])
}
