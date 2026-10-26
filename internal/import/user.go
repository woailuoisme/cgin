// Package importer 提供领域实体的流式 Excel 导入用例。
package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"

	"cgin/internal/enum"
	"cgin/internal/user"
	"cgin/pkg/excel"
)

const (
	// userChunkSize 是单次分块落库的默认批量大小。
	userChunkSize = 500
)

// UserRow 表示从 Excel 单行解码并归一化后的用户导入模型。
type UserRow struct {
	Name   string
	Email  string
	Role   enum.Role
	Status enum.UserStatus
}

// userColumns 定义用户导入的声明式强类型列契约，表头命名与 internal/export 对齐。
var userColumns = []excel.Column[UserRow]{
	{
		Header:   "Name",
		Required: true,
		Decode: func(cell string, dst *UserRow) error {
			trimmed := strings.TrimSpace(cell)
			if len(trimmed) > 100 {
				return errors.New("name must not exceed 100 characters")
			}
			dst.Name = trimmed
			return nil
		},
	},
	{
		Header:   "Email",
		Required: true,
		Decode: func(cell string, dst *UserRow) error {
			norm := user.NormaliseEmail(cell)
			addr, err := mail.ParseAddress(norm)
			if err != nil || addr.Address != norm {
				return errors.New("invalid email address format")
			}
			dst.Email = norm
			return nil
		},
	},
	{
		Header:   "Role",
		Required: false,
		Decode: func(cell string, dst *UserRow) error {
			role := enum.Role(strings.ToLower(strings.TrimSpace(cell)))
			if !role.IsValid() {
				return fmt.Errorf("invalid role %q", cell)
			}
			dst.Role = role
			return nil
		},
	},
	{
		Header:   "Status",
		Required: false,
		Decode: func(cell string, dst *UserRow) error {
			status := enum.UserStatus(strings.ToLower(strings.TrimSpace(cell)))
			if !status.IsValid() {
				return fmt.Errorf("invalid user status %q", cell)
			}
			dst.Status = status
			return nil
		},
	},
}

// Users 流式解析并校验用户导入工作簿，对缺失的可选枚举填充默认活跃成员状态，
// 强制执行文件内邮箱唯一去重，并按分块回调 saveBatch。
func Users(
	ctx context.Context,
	r io.Reader,
	saveBatch func(ctx context.Context, rows []UserRow) error,
	opts ...excel.ReadOption[UserRow],
) error {
	if saveBatch == nil {
		return errors.New("importer: nil saveBatch callback")
	}

	baseOpts := []excel.ReadOption[UserRow]{
		excel.WithChunkSize[UserRow](userChunkSize),
		excel.WithUniqueKey("email", func(row *UserRow) string {
			return row.Email
		}),
	}
	baseOpts = append(baseOpts, opts...)

	return excel.ReadTable(ctx, r, userColumns, func(batchCtx context.Context, rows []UserRow) error {
		for i := range rows {
			if rows[i].Role == "" {
				rows[i].Role = enum.RoleUser
			}
			if rows[i].Status == "" {
				rows[i].Status = enum.StatusActive
			}
		}
		return saveBatch(batchCtx, rows)
	}, baseOpts...)
}
