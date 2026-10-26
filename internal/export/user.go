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

// userColumns 定义用户导出的声明式强类型列契约。
var userColumns = []excel.Column[sqlc.User]{
	{
		Header: "ID",
		Encode: func(u *sqlc.User) any { return u.ID },
	},
	{
		Header: "Name",
		Encode: func(u *sqlc.User) any { return u.Name },
	},
	{
		Header: "Email",
		Encode: func(u *sqlc.User) any { return u.Email },
	},
	{
		Header: "Role",
		Encode: func(u *sqlc.User) any { return u.Role },
	},
	{
		Header: "Status",
		Encode: func(u *sqlc.User) any { return u.Status },
	},
	{
		Header: "Created At",
		Encode: func(u *sqlc.User) any {
			if u.CreatedAt.IsZero() {
				return ""
			}
			return u.CreatedAt.UTC().Format(time.RFC3339)
		},
	},
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

	next := func() (sqlc.User, bool, error) {
		for cursor >= len(buffer) {
			if err := ctx.Err(); err != nil {
				return sqlc.User{}, false, err
			}
			if ended {
				return sqlc.User{}, false, nil
			}

			chunk, err := fetch(ctx, filter, afterID, userChunkSize)
			if err != nil {
				return sqlc.User{}, false, err
			}
			if len(chunk) == 0 {
				return sqlc.User{}, false, nil
			}
			if len(chunk) < userChunkSize {
				ended = true
			}

			buffer = chunk
			cursor = 0
			afterID = chunk[len(chunk)-1].ID
		}

		entity := buffer[cursor]
		cursor++
		return entity, true, nil
	}

	return excel.WriteTable(ctx, w, userSheet, userColumns, next)
}
