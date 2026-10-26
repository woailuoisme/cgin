package search

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/samber/lo"
)

// ChunkHandler 接收分批搜索文档切片的回调函数。
type ChunkHandler func(docs []map[string]any) error

// BatchImporter 定义分批读取模型数据并投递文档的函数契约。
//
// 数据源（连接池、仓储等）由装配方在构造闭包时捕获，平台层不感知具体存储实现。
type BatchImporter func(ctx context.Context, batchSize int, handler ChunkHandler) (int64, error)

// RecordCounter 定义统计模型文档总量的函数契约。
type RecordCounter func(ctx context.Context) (int64, error)

// ModelDescriptor 记录可搜索模型的元数据与导入器。
type ModelDescriptor struct {
	Name      string
	IndexName string
	Importer  BatchImporter
	Counter   RecordCounter
}

// Catalog 是显式装配的可搜索模型集合，支持按模型名或索引名检索。
//
// 集合在装配期一次性构建，运行期只读，因此天然并发安全。
type Catalog struct {
	byKey map[string]ModelDescriptor
	all   []ModelDescriptor
}

// NewCatalog 构造模型集合：按索引名去重并按索引名排序。
func NewCatalog(models ...ModelDescriptor) *Catalog {
	unique := lo.UniqBy(
		lo.Filter(models, func(m ModelDescriptor, _ int) bool {
			return strings.TrimSpace(m.IndexName) != ""
		}),
		func(m ModelDescriptor) string { return strings.ToLower(m.IndexName) },
	)
	slices.SortFunc(unique, func(a, b ModelDescriptor) int {
		return cmp.Compare(a.IndexName, b.IndexName)
	})

	byKey := make(map[string]ModelDescriptor, len(unique)*2)
	for _, model := range unique {
		byKey[strings.ToLower(model.Name)] = model
		byKey[strings.ToLower(model.IndexName)] = model
	}

	return &Catalog{byKey: byKey, all: unique}
}

// Lookup 按模型名或索引名查询；未注册时返回 false。
func (c *Catalog) Lookup(name string) (ModelDescriptor, bool) {
	if c == nil {
		return ModelDescriptor{}, false
	}
	desc, ok := c.byKey[strings.ToLower(strings.TrimSpace(name))]
	return desc, ok
}

// All 返回按索引名排序的全部模型（返回副本，调用方无法篡改集合）。
func (c *Catalog) All() []ModelDescriptor {
	if c == nil {
		return nil
	}
	return slices.Clone(c.all)
}
