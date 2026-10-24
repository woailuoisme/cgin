package search_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/search"
)

// SampleHit 定义测试用强类型搜索结果文档结构。
type SampleHit struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Score int    `json:"score"`
}

// TestSearch_DriverRegistry 验证全文搜索驱动工厂注册与扩展机制 (OCP)。
func TestSearch_DriverRegistry(t *testing.T) {
	customFactoryCalled := false
	search.RegisterDriver("custom_test", func(cfg config.SearchConfig) (search.Client, error) {
		customFactoryCalled = true
		return search.NewNoop(), nil
	})

	// 1. 成功调用自定义注册的驱动
	client, err := search.New(config.SearchConfig{
		Driver: "custom_test",
		Meilisearch: config.MeilisearchConfig{
			Host: "http://127.0.0.1:7700",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.True(t, customFactoryCalled)

	// 2. 未注册驱动返回明确的不支持错误
	_, err = search.New(config.SearchConfig{
		Driver: "unsupported_engine",
		Meilisearch: config.MeilisearchConfig{
			Host: "http://127.0.0.1:7700",
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported search driver: unsupported_engine")
}

// TestSearch_TypedSearchResult 验证泛型搜索与直接解码契约。
func TestSearch_TypedSearchResult(t *testing.T) {
	ctx := t.Context()
	client := search.NewNoop()
	idx := client.Index("articles")

	// 1. 验证 SearchTyped 对 noopIndex（实现 TypedSearcher）的零分配安全返回
	typedRes, err := search.SearchTyped[SampleHit](ctx, idx, "golang", search.WithLimit(10))
	require.NoError(t, err)
	require.NotNil(t, typedRes)
	assert.Empty(t, typedRes.Hits)
	assert.Zero(t, typedRes.EstimatedTotalHits)
	assert.Equal(t, int64(0), typedRes.ProcessingTimeMs)

	// 2. 验证 nil Index 时的容错处理
	nilRes, err := search.SearchTyped[SampleHit](ctx, nil, "golang")
	require.NoError(t, err)
	require.NotNil(t, nilRes)
	assert.Empty(t, nilRes.Hits)

	// 3. 验证 DecodeHits 将 map 切片解码为强类型结构体
	rawResult := &search.SearchResult{
		Hits: []map[string]any{
			{"id": "doc_1", "title": "First Article", "score": 98},
			{"id": "doc_2", "title": "Second Article", "score": 85},
		},
		EstimatedTotalHits: 2,
	}

	decoded, err := search.DecodeHits[SampleHit](rawResult)
	require.NoError(t, err)
	require.Len(t, decoded, 2)
	assert.Equal(t, "doc_1", decoded[0].ID)
	assert.Equal(t, "First Article", decoded[0].Title)
	assert.Equal(t, 98, decoded[0].Score)
	assert.Equal(t, "doc_2", decoded[1].ID)

	// 4. 空结果或 nil 结果的边界解码
	emptyDecoded, err := search.DecodeHits[SampleHit](nil)
	require.NoError(t, err)
	assert.Empty(t, emptyDecoded)
}

// TestSearch_TypedSearcherRole 验证索引实现 TypedSearcher 角色接口 (ISP)。
func TestSearch_TypedSearcherRole(t *testing.T) {
	client := search.NewNoop()
	idx := client.Index("users")

	// 编译期与运行时接口断言
	ts, ok := idx.(search.TypedSearcher)
	require.True(t, ok)
	require.NotNil(t, ts)

	var target []SampleHit
	res, err := ts.SearchDirect(context.Background(), "query", &target)
	require.NoError(t, err)
	assert.NotNil(t, res)
	assert.Empty(t, target)
}
