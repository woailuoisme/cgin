package adminuser

import (
	"context"
	"io"
	"time"

	"cgin/internal/database/sqlc"
	"cgin/internal/platform/excel"
	"cgin/internal/shared/user"
)

const (
	// userExportSheet 是导出文件的工作表名。
	userExportSheet = "Users"
	// userExportChunkSize 是单次物理拉取的批量大小。
	userExportChunkSize = 500
)

// userExportHeader 是用户导出的固定表头。
var userExportHeader = []string{"ID", "Name", "Email", "Role", "Status", "Created At"}

// userExportRow 把用户实体投影为导出单元格，未设置的时间落成空单元格。
func userExportRow(entity *sqlc.User) []any {
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

// writeUsersXLSX 把符合条件的用户按键集游标分块拉取并流式写入 xlsx。
//
// 游标、缓冲与终止判定都收敛在闭包里：调用方只需给出拉取函数与过滤条件，
// 既不需要实现接口，也无法把状态从外部打乱。
func writeUsersXLSX(
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
			if ended {
				return nil, false, nil
			}

			chunk, err := fetch(ctx, filter, afterID, userExportChunkSize)
			if err != nil {
				return nil, false, err
			}
			if len(chunk) == 0 {
				return nil, false, nil
			}
			if len(chunk) < userExportChunkSize {
				ended = true
			}

			buffer = chunk
			cursor = 0
			afterID = chunk[len(chunk)-1].ID
		}

		row := userExportRow(&buffer[cursor])
		cursor++
		return row, true, nil
	}

	return excel.Write(ctx, w, userExportSheet, userExportHeader, next)
}
