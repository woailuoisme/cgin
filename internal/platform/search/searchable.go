package search

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/samber/lo"
	"github.com/uptrace/bun"
)

// Searchable 定义具备全文检索能力的实体模型契约。
type Searchable interface {
	// SearchableAs 返回该实体对应的全文搜索基础索引名称。
	SearchableAs() string
	// SearchableKey 返回该实体的唯一主键标识值。
	SearchableKey() string
	// ToSearchableArray 将模型属性转换为结构化的搜索文档字典。
	ToSearchableArray() map[string]any
}

// ChunkHandler 接收分批搜索文档切片的回调函数。
type ChunkHandler func(docs []map[string]any) error

// BatchImporter 定义从持久化数据源流式分批读取模型数据的函数契约。
type BatchImporter func(ctx context.Context, db bun.IDB, batchSize int, handler ChunkHandler) (int64, error)

// RecordCounter 定义统计模型数据总量的函数契约。
type RecordCounter func(ctx context.Context, db bun.IDB) (int64, error)

// ModelDescriptor 记录可搜索模型的元数据与导入器。
type ModelDescriptor struct {
	Name      string
	IndexName string
	Importer  BatchImporter
	Counter   RecordCounter
}

var (
	registryMu  sync.RWMutex
	descriptors = make(map[string]ModelDescriptor)
)

// RegisterModel 注册一个可搜索模型的导入器与元数据（可附加可选的总量统计器）。
func RegisterModel(name, indexName string, importer BatchImporter, counter ...RecordCounter) {
	registryMu.Lock()
	defer registryMu.Unlock()

	desc := ModelDescriptor{
		Name:      strings.ToLower(strings.TrimSpace(name)),
		IndexName: strings.ToLower(strings.TrimSpace(indexName)),
		Importer:  importer,
		Counter:   lo.FirstOrEmpty(counter),
	}

	descriptors[desc.Name] = desc
	descriptors[desc.IndexName] = desc
}

// GetModel 获取指定模型名称或索引名称对应的注册描述符。
func GetModel(name string) (ModelDescriptor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	desc, ok := descriptors[strings.ToLower(strings.TrimSpace(name))]
	return desc, ok
}

// RegisteredModels 返回所有已注册的唯一模型描述符列表（按索引名称排序）。
func RegisteredModels() []ModelDescriptor {
	registryMu.RLock()
	defer registryMu.RUnlock()

	list := lo.UniqBy(lo.Values(descriptors), func(d ModelDescriptor) string {
		return d.IndexName
	})

	slices.SortFunc(list, func(a, b ModelDescriptor) int {
		return cmp.Compare(a.IndexName, b.IndexName)
	})

	return list
}
