package admin

import (
	"context"
	"io"
	"strings"

	"github.com/samber/oops"

	"bgin/internal/database/models"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/excel"
	"bgin/internal/shared/enums"
)

// UsersImport 封装用户数据批量导入的解析与验证逻辑。
type UsersImport struct {
	importedCount int
}

// NewUsersImport 创建用户数据批量导入实例。
func NewUsersImport() *UsersImport {
	return &UsersImport{}
}

// HeadingRow 指定表头所在的行号（1-based）。
func (i *UsersImport) HeadingRow() int {
	return 1
}

// BatchSize 指定批量处理时的批次大小。
func (i *UsersImport) BatchSize() int {
	return 100
}

// ToModel 将单行单元格切片解析映射为 User 领域模型。
func (i *UsersImport) ToModel(row []string) (any, error) {
	if len(row) < 2 {
		return nil, oops.Code(string(errs.CodeValidationFailed)).
			Errorf("insufficient columns in row, expected at least Name and Email")
	}

	name := strings.TrimSpace(row[0])
	email := strings.TrimSpace(row[1])
	if name == "" || email == "" {
		return nil, oops.Code(string(errs.CodeValidationFailed)).
			Errorf("name and email cannot be empty")
	}

	user := &models.User{
		Name:   name,
		Email:  email,
		Role:   models.RoleUser,
		Status: enums.StatusActive,
	}

	i.importedCount++
	return user, nil
}

// ImportedCount 返回本次成功解析并完成字段校验的记录数（已通过格式与必填字段验证的解析行数）。
func (i *UsersImport) ImportedCount() int {
	return i.importedCount
}

// ImportStream 辅助函数：从指定的 Reader 中流式读取 Excel 并解析用户模型。
func ImportStream(ctx context.Context, r io.Reader) (*UsersImport, error) {
	imp := NewUsersImport()
	if err := excel.Import(ctx, imp, r); err != nil {
		return nil, err
	}
	return imp, nil
}

// 确保 UsersImport 实现了标准 Excel 导入协议的各类接口规范。
var (
	_ excel.ToModel          = (*UsersImport)(nil)
	_ excel.WithHeadingRow   = (*UsersImport)(nil)
	_ excel.WithBatchInserts = (*UsersImport)(nil)
)
