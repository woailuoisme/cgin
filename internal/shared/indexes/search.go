// Package indexes 提供应用层业务实体的全文检索索引器定义与集中装配。
package indexes

import "sync"

var registerOnce sync.Once

// RegisterAll 集中装配所有应用业务模型的全文检索索引器。
func RegisterAll() {
	registerOnce.Do(func() {
		RegisterUserIndexer()
	})
}

func init() {
	RegisterAll()
}
