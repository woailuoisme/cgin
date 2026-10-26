// Package index 提供应用层业务实体的全文检索索引器装配。
package index

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/pkg/search"
)

// defaultBatchSize 默认批量检索导入批次大小。
const defaultBatchSize = 100

// Names 返回可搜索模型名，供 CLI 参数补全与最小校验使用。
func Names() []string {
	return []string{"user"}
}

// Catalog 装配全部可搜索模型。
//
// db 只在真正执行导入时被使用；仅需元数据（如索引清空）的场景可传 nil。
func Catalog(db *database.DB) *search.Catalog {
	return search.NewCatalog(userIndexer(db))
}

// userIndexer 装配 User 模型的检索导入器，数据源由 db 闭包捕获。
func userIndexer(db *database.DB) search.ModelDescriptor {
	return search.ModelDescriptor{
		Name:      "user",
		IndexName: "users",
		Importer: func(ctx context.Context, batchSize int, handler search.ChunkHandler) (int64, error) {
			return importUsers(ctx, db, batchSize, handler)
		},
		Counter: func(ctx context.Context) (int64, error) {
			return countUsers(ctx, db)
		},
	}
}

// countUsers 统计 User 实体的总记录数。
func countUsers(ctx context.Context, db *database.DB) (int64, error) {
	if db == nil {
		return 0, errors.New("invalid database instance provided for user count")
	}
	return db.Queries(ctx).CountActiveUsers(ctx)
}

// importUsers 按 id 键集分批读取用户数据并转换为检索文档字典。
func importUsers(ctx context.Context, db *database.DB, batchSize int, handler search.ChunkHandler) (int64, error) {
	if db == nil {
		return 0, errors.New("invalid database instance provided for search import")
	}
	batchSize = lo.Ternary(batchSize <= 0, defaultBatchSize, batchSize)

	count, err := countUsers(ctx, db)
	if err != nil || count == 0 {
		return count, err
	}

	var (
		lastID  string
		current int64
	)
	for {
		users, err := db.Queries(ctx).ListUsersForSearch(ctx, sqlc.ListUsersForSearchParams{
			AfterID:   lastID,
			PageLimit: int64(batchSize),
		})
		if err != nil {
			return 0, fmt.Errorf("failed to query users for search import: %w", err)
		}
		if len(users) == 0 {
			break
		}

		docs := lo.Map(users, func(u sqlc.User, _ int) map[string]any {
			return searchDocument(&u)
		})

		if err := handler(docs); err != nil {
			return 0, fmt.Errorf("failed to handle search documents chunk: %w", err)
		}

		current += int64(len(users))
		lastID = users[len(users)-1].ID
		if len(users) < batchSize {
			break
		}
	}

	return current, nil
}

// searchDocument 把用户行投影为检索文档。
func searchDocument(u *sqlc.User) map[string]any {
	doc := map[string]any{
		"id":             u.ID,
		"name":           u.Name,
		"email":          u.Email,
		"role":           string(u.Role),
		"status":         string(u.Status),
		"email_verified": u.EmailVerified,
		"created_at":     u.CreatedAt.UTC().Unix(),
		"updated_at":     u.UpdatedAt.UTC().Unix(),
	}
	if u.Username != nil {
		doc["username"] = *u.Username
	}
	if u.DisplayUsername != nil {
		doc["display_username"] = *u.DisplayUsername
	}
	if u.PhoneNumber != nil {
		doc["phone_number"] = *u.PhoneNumber
	}
	return doc
}
