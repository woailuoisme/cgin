package export

import (
	"context"
	"io"
	"time"

	"cgin/internal/database/sqlc"
	"cgin/internal/user"
	"cgin/pkg/excel"
)

const (
	// userSheet 是用户导出文件的工作表名。
	userSheet = "Users"
	// userChunkSize 是单次物理拉取的批量大小。
	userChunkSize = 500
)

// userHeader 是用户导出的固定表头。
var userHeader = []string{"ID", "Name", "Email", "Role", "Status", "Created At"}

// userRow 把用户实体投影为导出单元格，未设置的时间落成空单元格。
func userRow(entity *sqlc.User) []any {
	var createdAt string
	if !entity.CreatedAt.IsZero() {
		createdAt = entity.CreatedAt.UTC().Format(time.RFC3339)
	}

	return []any{
		entity.ID,
		entity.Name,
		entity.Email,
		entity.Role,
		entity.Status,
		createdAt,
	}
}

// Users 把符合条件的用户按键集游标分块拉取并流式写入 xlsx。
//
// 游标、缓冲与终止判定都收敛在闭包里：调用方只需给出拉取函数与过滤条件，
// 既不需要实现接口，也无法把状态从外部打乱。
func Users(
	ctx context.Context,
	w io.Writer,
	fetch func(ctx context.Context, filter user.ListFilter, afterID string, limit int) ([]sqlc.User, error),
	filter user.ListFilter,
) error {
	var (
		afterID string
		buffer  []sqlc.User
		cursor  int
		ended   bool
	)

	next := func() ([]any, bool, error) {
		for cursor >= len(buffer) {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			if ended {
				return nil, false, nil
			}

			chunk, err := fetch(ctx, filter, afterID, userChunkSize)
			if err != nil {
				return nil, false, err
			}
			if len(chunk) == 0 {
				return nil, false, nil
			}
			if len(chunk) < userChunkSize {
				ended = true
			}

			buffer = chunk
			cursor = 0
			afterID = chunk[len(chunk)-1].ID
		}

		row := userRow(&buffer[cursor])
		cursor++
		return row, true, nil
	}

	return excel.Write(ctx, w, userSheet, userHeader, next)
}
