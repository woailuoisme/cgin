package admin

import (
	"context"
	"io"
	"slices"
	"time"

	"bgin/internal/database/models"
	"bgin/internal/platform/excel"
	"bgin/internal/shared/user"
)

// UserFetcher 声明按条件分页物理拉取用户实体的抽象契约（ISP），由 models 或函数适配器满足。
type UserFetcher interface {
	List(ctx context.Context, filter user.ListFilter, offset, limit int) ([]models.User, error)
}

// UserFetchFunc 是 UserFetcher 的函数式适配器。
type UserFetchFunc func(ctx context.Context, filter user.ListFilter, offset, limit int) ([]models.User, error)

// List 实现 UserFetcher 接口。
func (f UserFetchFunc) List(ctx context.Context, filter user.ListFilter, offset, limit int) ([]models.User, error) {
	return f(ctx, filter, offset, limit)
}

// userExportMetadata 提供用户 Excel 导出的通用表头、行映射与表名。
type userExportMetadata struct{}

// Headings 定义 Excel 导出表头。
func (userExportMetadata) Headings() []string {
	return slices.Clone(defaultUserHeadings)
}

// Mapping 将单条 User 实体映射为单元格数据行。
func (userExportMetadata) Mapping(row any) []any {
	return mapUserRow(row)
}

// Title 定义 Excel 工作表名称。
func (userExportMetadata) Title() string {
	return "Users"
}

// UsersExport 封装用户数据列表导出为 Excel 文件的声明与格式化映射。
type UsersExport struct {
	userExportMetadata
	users []*models.User
}

// NewUsersExport 创建用户数据导出定义实例。
// 采用防御性拷贝隔离输入切片，遵循 Uber Go 边界规范。
func NewUsersExport(users []*models.User) *UsersExport {
	return &UsersExport{users: slices.Clone(users)}
}

// FromSlice 返回待导出的切片数据源。
// 返回防御性拷贝，防止外部消费方修改切片底层元素。
func (e *UsersExport) FromSlice() any {
	return slices.Clone(e.users)
}

// defaultUserHeadings 用户导出默认表头定义。
var defaultUserHeadings = []string{"ID", "Name", "Email", "Role", "Status", "Created At"}

func userExportOptions(opts []excel.ExportOption) []excel.ExportOption {
	return append([]excel.ExportOption{excel.WithSheetName("Users")}, opts...)
}

func mapUserModel(u *models.User) []any {
	return mapUserRow(u)
}

// ExportStream 辅助函数：将用户切片直接流式序列化并写入指定的 io.Writer。
func ExportStream(ctx context.Context, w io.Writer, users []*models.User, opts ...excel.ExportOption) error {
	return excel.ExportSlice(ctx, w, users, defaultUserHeadings, mapUserModel, userExportOptions(opts)...)
}

const defaultUserChunkSize = 500

// UsersChunkedExport 封装基于物理分页查询的用户批量流式导出器，严格实现 excel.FromChunkedQuery。
// 借助 StreamWriter 与分批数据库拉取，保证在海量用户导出场景下内存维持常数阶 O(1)，杜绝 OOM。
type UsersChunkedExport struct {
	userExportMetadata
	fetcher   UserFetcher
	filter    user.ListFilter
	chunkSize int
}

// NewUsersChunkedExport 创建基于物理分块查询的用户导出器。
func NewUsersChunkedExport(fetcher UserFetcher, filter user.ListFilter, chunkSize ...int) *UsersChunkedExport {
	size := defaultUserChunkSize
	if len(chunkSize) > 0 && chunkSize[0] > 0 {
		size = chunkSize[0]
	}
	return &UsersChunkedExport{
		fetcher:   fetcher,
		filter:    filter,
		chunkSize: size,
	}
}

// ChunkSize 返回单批次数据库物理分页拉取的记录条数。
func (e *UsersChunkedExport) ChunkSize() int {
	return e.chunkSize
}

// FetchChunk 按偏移量分批物理拉取用户数据，供 Excel 流式写入器消费。
func (e *UsersChunkedExport) FetchChunk(ctx context.Context, offset int, limit int) ([]any, error) {
	if e.fetcher == nil {
		return nil, nil
	}
	users, err := e.fetcher.List(ctx, e.filter, offset, limit)
	if err != nil || len(users) == 0 {
		return nil, err
	}

	rows := make([]any, len(users))
	for i := range users {
		rows[i] = &users[i]
	}
	return rows, nil
}

// ExportChunkedStream 辅助函数：将查询条件分块物理拉取并直接流式序列化写入指定的 io.Writer。
func ExportChunkedStream(
	ctx context.Context,
	w io.Writer,
	fetcher UserFetcher,
	filter user.ListFilter,
	opts ...excel.ExportOption,
) error {
	size := defaultUserChunkSize
	return excel.NewExportBuilder[*models.User]().
		WithHeadings(defaultUserHeadings...).
		WithMapping(mapUserModel).
		FromGenerator(func(yield func(item *models.User) bool) error {
			if fetcher == nil {
				return nil
			}
			offset := 0
			for {
				chunk, err := fetcher.List(ctx, filter, offset, size)
				if err != nil {
					return err
				}
				if len(chunk) == 0 {
					break
				}
				for i := range chunk {
					if !yield(&chunk[i]) {
						return nil
					}
				}
				if len(chunk) < size {
					break
				}
				offset += len(chunk)
			}
			return nil
		}).
		WithOptions(userExportOptions(opts)...).
		Write(ctx, w)
}

func mapUserRow(row any) []any {
	var target *models.User
	switch u := row.(type) {
	case *models.User:
		target = u
	case models.User:
		target = &u
	default:
		return nil
	}

	if target == nil {
		return nil
	}

	createdAtStr := ""
	if !target.CreatedAt.IsZero() {
		createdAtStr = target.CreatedAt.Format(time.RFC3339)
	}

	return []any{
		target.ID,
		target.Name,
		target.Email,
		target.Role,
		target.Status,
		createdAtStr,
	}
}

// 确保 UsersExport 与 UsersChunkedExport 实现了标准 Excel 导出协议的接口规范。
var (
	_ excel.FromSlice        = (*UsersExport)(nil)
	_ excel.WithHeadings     = (*UsersExport)(nil)
	_ excel.WithMapping      = (*UsersExport)(nil)
	_ excel.WithTitle        = (*UsersExport)(nil)
	_ excel.FromChunkedQuery = (*UsersChunkedExport)(nil)
	_ excel.WithHeadings     = (*UsersChunkedExport)(nil)
	_ excel.WithMapping      = (*UsersChunkedExport)(nil)
	_ excel.WithTitle        = (*UsersChunkedExport)(nil)
)
