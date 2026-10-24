package indexes

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"
	"github.com/uptrace/bun"

	"bgin/internal/database/models"
	"bgin/internal/platform/search"
)

// defaultBatchSize 默认批量检索导入批次大小。
const defaultBatchSize = 100

// CountUsersForSearch 统计 User 实体的总记录数。
func CountUsersForSearch(ctx context.Context, db bun.IDB) (int64, error) {
	if db == nil {
		return 0, errors.New("invalid bun database instance provided for user count")
	}
	return db.NewSelect().Model((*models.User)(nil)).Count(ctx)
}

// RegisterUserIndexer 向全局检索注册表注册 User 模型的导入器与元数据。
func RegisterUserIndexer() {
	search.RegisterModel("user", "users", ImportUsersForSearch, CountUsersForSearch)
}

// ImportUsersForSearch 分批从数据库读取用户数据并转换为检索文档字典。
func ImportUsersForSearch(ctx context.Context, db bun.IDB, batchSize int, handler search.ChunkHandler) (int64, error) {
	if db == nil {
		return 0, errors.New("invalid bun database instance provided for search import")
	}
	batchSize = lo.Ternary(batchSize <= 0, defaultBatchSize, batchSize)

	count, err := CountUsersForSearch(ctx, db)
	if err != nil || count == 0 {
		return count, err
	}

	var (
		lastID  string
		current int64
	)
	for {
		var users []*models.User
		query := db.NewSelect().
			Model(&users).
			OrderExpr("? ASC", bun.Ident("u.id")).
			Limit(int64(batchSize))
		if lastID != "" {
			query.Where("u.id > ?", lastID)
		}
		if err := query.Scan(ctx); err != nil {
			return 0, fmt.Errorf("failed to query users for search import: %w", err)
		}
		if len(users) == 0 {
			break
		}

		docs := lo.Map(users, func(u *models.User, _ int) map[string]any {
			return u.ToSearchableArray()
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
