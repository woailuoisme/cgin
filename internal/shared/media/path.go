// Package media 提供存储分区路径规划策略。
package media

import (
	"cmp"
	"context"
	"path"
	"strings"
	"time"

	"github.com/samber/lo"
)

// CategoryDatePathStrategy 将文件组织为 uploads/{category}/{YYYYMMDD}/{filename} 分区目录。
type CategoryDatePathStrategy struct {
	BasePrefix string
}

// NewCategoryDatePathStrategy 创建带可选基础前缀（默认 "uploads"）的 CategoryDatePathStrategy 实例。
func NewCategoryDatePathStrategy(basePrefix ...string) *CategoryDatePathStrategy {
	return &CategoryDatePathStrategy{BasePrefix: cleanBasePrefix(basePrefix...)}
}

// GeneratePath 生成分区存储相对路径：uploads/{category}/{YYYYMMDD}/{filename}。
func (s *CategoryDatePathStrategy) GeneratePath(_ context.Context, category, fileName string) string {
	category = cmp.Or(strings.Trim(strings.ToLower(strings.TrimSpace(category)), "/"), "default")

	datePart := time.Now().UTC().Format("20060102")
	return path.Join(s.BasePrefix, category, datePart, fileName)
}

// DatePathStrategy 将文件组织为按日期分区的 uploads/{YYYYMMDD}/{filename} 目录。
type DatePathStrategy struct {
	BasePrefix string
}

// NewDatePathStrategy 创建带可选基础前缀的 DatePathStrategy 实例。
func NewDatePathStrategy(basePrefix ...string) *DatePathStrategy {
	return &DatePathStrategy{BasePrefix: cleanBasePrefix(basePrefix...)}
}

// GeneratePath 生成按日期分区的相对存储路径：uploads/{YYYYMMDD}/{filename}。
func (s *DatePathStrategy) GeneratePath(_ context.Context, _, fileName string) string {
	datePart := time.Now().UTC().Format("20060102")
	return path.Join(s.BasePrefix, datePart, fileName)
}

// cleanBasePrefix 清洗并获取基础路径前缀（缺省为 "uploads"）。
func cleanBasePrefix(basePrefix ...string) string {
	clean := strings.Trim(strings.TrimSpace(lo.FirstOrEmpty(basePrefix)), "/")
	return cmp.Or(clean, "uploads")
}
