// Package excel 提供 Excel/CSV 的流式导入导出能力。
//
// 导出存在两条互不重叠的入口，按"数据来源是否在编译期已知"二选一：
//
//   - 泛型流式入口（stream.go）：ExportSlice / ExportIterator / ExportBuilder / StreamExport，
//     数据是已定型的内存切片或迭代器，类型在编译期即可校验；
//   - 反射管道入口（export.go，经 Service 暴露）：导出对象在运行期确定（SQL 游标、分块查询、
//     动态列与样式），支持多工作表，内存占用与结果集大小无关。
//
// 导入统一走 Concern 协议式入口（import.go / stream.go）：实现 ToModel、WithHeadingRow 等标记接口即可。
// 调用点请勿在同一链路里混用两条导出入口。
package excel

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"io"
	"sync"

	"github.com/samber/lo"
	"github.com/samber/oops"
	"github.com/xuri/excelize/v2"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
)

// ExportConcern 标记与导出操作相关的 Concern 接口契约。
type ExportConcern any

// ImportConcern 标记与导入操作相关的 Concern 接口契约。
type ImportConcern any

// FromSlice 声明导出数据来自内存切片。
type FromSlice interface {
	ExportConcern
	FromSlice() any
}

// FromQuery 声明导出数据通过 SQL 数据库游标流式获取，保持恒定低内存。
type FromQuery interface {
	ExportConcern
	QueryRows(ctx context.Context, db *sql.DB) (*sql.Rows, error)
}

// WithHeadings 提供自定义列标题切片。
type WithHeadings interface {
	ExportConcern
	Headings() []string
}

// WithMapping 定义单行数据到 Excel 单元格切片的显式转换逻辑。
type WithMapping interface {
	ExportConcern
	Mapping(row any) []any
}

// ShouldAutoSize 声明是否启用自动列宽计算。
type ShouldAutoSize interface {
	ExportConcern
	ShouldAutoSize() bool
}

// WithColumnWidths 显式定义特定列的像素/字符宽度。
type WithColumnWidths interface {
	ExportConcern
	ColumnWidths() map[string]float64
}

// WithTitle 定义导出的自定义工作表名称。
type WithTitle interface {
	ExportConcern
	Title() string
}

// WithSheetChunkSize 配置每个工作表的最大行数阈值（超限自动新建分表）。
type WithSheetChunkSize interface {
	ExportConcern
	SheetChunkSize() int
}

// WithMultipleSheets 允许在单个导出工作簿中生成多个子工作表（对应 Laravel-Excel WithMultipleSheets）。
type WithMultipleSheets interface {
	ExportConcern
	Sheets() []any
}

// WithStyles 允许深度自定义工作簿与工作表样式（对应 Laravel-Excel WithStyles）。
type WithStyles interface {
	ExportConcern
	Styles(f *excelize.File, sheet string) error
}

// WithHeaderStyle 声明表头是否启用优雅预设样式（粗体、居中、浅灰背景 #F4F5F7、下边框）。
type WithHeaderStyle interface {
	ExportConcern
	HeaderStyle() *excelize.Style
}

// WithFreezePanes 声明是否冻结表头首行（行号通常为 1 或自定义）。
type WithFreezePanes interface {
	ExportConcern
	FreezePanes() (col int, row int)
}

// WithColumnFormatting 声明特定列的单元格格式（如 NumberFormat，对应 Laravel-Excel WithColumnFormatting）。
type WithColumnFormatting interface {
	ExportConcern
	ColumnFormats() map[string]string // 例如 map[string]string{"A": "@", "B": "yyyy-mm-dd", "C": "#,##0.00"}
}

// RawFormula 包装需要作为原始 Excel 公式执行的值，避免被公式注入安全过滤器转义。
type RawFormula string

// --- 导入 Concerns ---

// ToSlice 声明将解析后的 Excel 数据反序列化到目标切片中。
type ToSlice interface {
	ImportConcern
	ToSlice() any
}

// ToModel 定义将单行单元格字符串转换为领域/数据库实体模型。
type ToModel interface {
	ImportConcern
	ToModel(row []string) (any, error)
}

// OnRow 提供轻量级逐行回调处理函数。
type OnRow interface {
	ImportConcern
	OnRow(row []string) error
}

// WithHeadingRow 指定表头所在的行号（1-based）。
type WithHeadingRow interface {
	ImportConcern
	HeadingRow() int
}

// WithValidation 提供对解析后行数据或模型的业务规则校验。
type WithValidation interface {
	ImportConcern
	Validate(model any) error
}

// WithBatchInserts 配置批量持久化写入时的批次大小。
type WithBatchInserts interface {
	ImportConcern
	BatchSize() int
}

// BatchInserter 提供批量写入数据库实体的持久化接口。
type BatchInserter interface {
	ImportConcern
	InsertBatch(ctx context.Context, batch []any) error
}

// FromChunkedQuery 声明支持物理分块/分页查询拉取导出数据。
// 适用于海量数据导出，分批拉取释放数据库连接与网络缓冲，维持常数级内存。
type FromChunkedQuery interface {
	ExportConcern
	ChunkSize() int
	FetchChunk(ctx context.Context, offset int, limit int) ([]any, error)
}

// WithQueueNotification 定义队列异步任务完成或失败时的回调通知接口。
type WithQueueNotification interface {
	OnQueueSuccess(ctx context.Context, fileURL string) error
	OnQueueFailure(ctx context.Context, err error) error
}

// WithEvents 允许导出/导入生命周期事件挂载钩子（如用于后台任务进度上报）。
type WithEvents interface {
	OnProgress(processedRows int, totalRows int)
}

// WithChunkReading 声明分块读取或分批处理时的 Chunk 大小（默认 1000）。
type WithChunkReading interface {
	ImportConcern
	ChunkSize() int
}

// ToChunk 声明支持分批交付行数据（二维字符串切片），每批交付后底层清空复用切片，保证 O(1) 恒定内存。
type ToChunk interface {
	ImportConcern
	Chunk(ctx context.Context, rows [][]string) error
}

// WithUniqueCheck 声明启用基于布隆过滤器的两级去重防查库（Bloom 初筛 + 疑似重复时精准回源校验）。
// 仅当布隆过滤器判定"可能已存在"时才会触发 ExistsDB 回源验证，打消 99.9%+ 的数据库查询 I/O，并消除假阳性误判。
type WithUniqueCheck interface {
	ImportConcern
	// UniqueKey 从当前数据行提取唯一性标识键（如手机号、邮箱、订单号、联合主键）。返回空字符串则跳过该行去重。
	UniqueKey(row []string) string
	// ExistsDB 当布隆过滤器判定 key 可能已存在时触发二次精准回源校验（如查数据库或缓存），返回 true 表示确已存在。
	ExistsDB(ctx context.Context, key string) (bool, error)
}

// WithUniqueConfig 声明自定义布隆过滤器的预估容量与误判率。
// 若未实现，默认使用容量 1,000,000，误判率 0.0001 (0.01%)，仅占用约 2.3MB 内存。
type WithUniqueConfig interface {
	ImportConcern
	UniqueConfig() (expectedItems uint, falsePositiveRate float64)
}

// WithPreloadKeys 允许在流式解析 Excel 行之前，从数据库批量预热已知唯一键灌入布隆过滤器。
// 预热后未命中布隆过滤器的键 100% 不在数据库中，彻底免疫百万次无谓 SELECT 查库。
type WithPreloadKeys interface {
	ImportConcern
	PreloadKeys(ctx context.Context) ([]string, error)
}

// RowFailure 记录单行数据在解析或业务校验失败时的上下文信息。
type RowFailure struct {
	Row    int      // Excel 文件实际行号（1-based）
	Values []string // 当前行原始字符串切片
	Field  string   // 校验失败的具体字段（若有）
	Error  error    // 具体失败错误原因
}

func (f RowFailure) String() string {
	if f.Field != "" {
		return fmt.Sprintf("row %d [%s]: %v", f.Row, f.Field, f.Error)
	}
	return fmt.Sprintf("row %d: %v", f.Row, f.Error)
}

// SkipsOnFailure 声明在单行解析或校验出错时不熔断中断，而是记录并继续执行（对应 Laravel-Excel SkipsOnFailure）。
type SkipsOnFailure interface {
	ImportConcern
	OnFailure(failure RowFailure)
}

// WithMultipleSheetsImport 允许为不同工作表分配不同的导入处理器。
type WithMultipleSheetsImport interface {
	ImportConcern
	Sheets() map[string]any
}

// ImportResult 封装导入操作的统计信息与失败归集列表。
type ImportResult struct {
	TotalRows     int          // 总数据行数（不含表头与被跳过的空行）
	ImportedCount int          // 成功解析/持久化的行数
	SkippedCount  int          // 跳过的无效/失败行数
	Failures      []RowFailure // 校验或解析失败记录列表
}

// HasFailures 检查导入过程中是否产生任何失败记录。
func (r *ImportResult) HasFailures() bool {
	return r != nil && len(r.Failures) > 0
}

func (r *ImportResult) merge(other *ImportResult) {
	if other == nil {
		return
	}
	r.TotalRows += other.TotalRows
	r.ImportedCount += other.ImportedCount
	r.SkippedCount += other.SkippedCount
	r.Failures = append(r.Failures, other.Failures...)
}

// ExportOption 配置导出操作的函数式选项。
type ExportOption func(*exportConfig)

type exportConfig struct {
	sheetName        string
	db               *sql.DB
	autoWidth        *bool
	columnWidths     map[string]float64
	sheetChunkSize   int
	sanitizeFormulas bool
	headerStyle      *excelize.Style
	freezePanes      bool
	columnFormats    map[string]string
	headings         []string
	mapping          func(item any) []any
	maxRows          int
	onProgress       func(processedRows int)
}

func newDefaultExportConfig() *exportConfig {
	return &exportConfig{
		sheetName:        defaultSheetName,
		sheetChunkSize:   defaultMaxRowsPerSheet,
		sanitizeFormulas: true, // 默认开启 CWE-1236 公式注入防御
	}
}

// WithSheetName 覆盖导出时的默认工作表名称。
func WithSheetName(name string) ExportOption {
	return func(c *exportConfig) {
		if name != "" {
			c.sheetName = name
		}
	}
}

// WithDB 为流式数据库查询显式注入 SQL 数据库句柄。
func WithDB(db *sql.DB) ExportOption {
	return func(c *exportConfig) {
		c.db = db
	}
}

// WithAutoWidth 配置是否自动计算列宽。
func WithAutoWidth(auto bool) ExportOption {
	return func(c *exportConfig) {
		c.autoWidth = &auto
	}
}

// WithCustomColumnWidths 显式覆盖列宽映射。
func WithCustomColumnWidths(widths map[string]float64) ExportOption {
	return func(c *exportConfig) {
		c.columnWidths = widths
	}
}

// WithMaxRowsPerSheet 覆盖每个工作表的最大记录行数限制。
func WithMaxRowsPerSheet(size int) ExportOption {
	return func(c *exportConfig) {
		if size > 0 {
			c.sheetChunkSize = size
		}
	}
}

// WithSanitizeFormulas 配置是否对单元格字符串前缀执行公式转义（防注入）。
func WithSanitizeFormulas(sanitize bool) ExportOption {
	return func(c *exportConfig) {
		c.sanitizeFormulas = sanitize
	}
}

// WithHeaderStyleOption 为表头启用或配置自定义样式。
func WithHeaderStyleOption(style *excelize.Style) ExportOption {
	return func(c *exportConfig) {
		c.headerStyle = style
	}
}

// WithFreezeHeader 配置是否自动冻结表头首行。
func WithFreezeHeader(freeze bool) ExportOption {
	return func(c *exportConfig) {
		c.freezePanes = freeze
	}
}

// WithColumnFormatsOption 配置特定列的 NumberFormat 样式。
func WithColumnFormatsOption(formats map[string]string) ExportOption {
	return func(c *exportConfig) {
		c.columnFormats = formats
	}
}

// WithHeadingsOption 显式指定表头切片。
func WithHeadingsOption(headings ...string) ExportOption {
	return func(c *exportConfig) {
		c.headings = headings
	}
}

// WithMappingOption 显式指定行数据映射函数。
func WithMappingOption(fn func(item any) []any) ExportOption {
	return func(c *exportConfig) {
		c.mapping = fn
	}
}

// WithMaxExportRows 设置最大允许导出的数据行数上限（防御恶意或无限制全表拖库）。
func WithMaxExportRows(max int) ExportOption {
	return func(c *exportConfig) {
		if max > 0 {
			c.maxRows = max
		}
	}
}

// WithExportProgress 注册导出进度回调函数。
func WithExportProgress(fn func(processedRows int)) ExportOption {
	return func(c *exportConfig) {
		c.onProgress = fn
	}
}

const (
	defaultMemorySpoolThreshold = 10 * 1024 * 1024  // 10MB: 小于 10MB 内存处理，大于 10MB 自动溢出到磁盘临时文件
	defaultMaxFileSize          = 50 * 1024 * 1024  // 50MB: 上传文件物理大小上限
	defaultUnzipSizeLimit       = 256 * 1024 * 1024 // 256MB: 解压安全上限（防止 Zip Bomb / XML Entity Expansion）
	defaultUnzipXMLSizeLimit    = 256 * 1024 * 1024 // 256MB: XML 单文件解压上限
	defaultChunkSize            = 1000              // 默认批次大小
)

// ImportOption 配置导入操作的函数式选项。
type ImportOption func(*importConfig)

type importConfig struct {
	sheetName            string
	headerRowIndex       int
	skipInvalidRows      bool
	batchSize            int
	maxRows              int
	maxFileSize          int64
	memorySpoolThreshold int64
	unzipSizeLimit       int64
	unzipXMLSizeLimit    int64
	onProgress           func(processedRows int, totalRows int)
}

func newDefaultImportConfig() *importConfig {
	return &importConfig{
		sheetName:            defaultSheetName,
		headerRowIndex:       1,
		maxFileSize:          defaultMaxFileSize,
		memorySpoolThreshold: defaultMemorySpoolThreshold,
		unzipSizeLimit:       defaultUnzipSizeLimit,
		unzipXMLSizeLimit:    defaultUnzipXMLSizeLimit,
	}
}

// WithImportSheetName 指定导入的目标工作表名称。
func WithImportSheetName(name string) ImportOption {
	return func(c *importConfig) {
		if name != "" {
			c.sheetName = name
		}
	}
}

// WithHeadingRowIndex 配置跳过的表头行数或表头所在行（从 1 开始计）。
func WithHeadingRowIndex(idx int) ImportOption {
	return func(c *importConfig) {
		if idx >= 0 {
			c.headerRowIndex = idx
		}
	}
}

// WithSkipInvalidRows 设置是否跳过无效或校验失败的数据行（收集至 ImportResult）。
func WithSkipInvalidRows(skip bool) ImportOption {
	return func(c *importConfig) {
		c.skipInvalidRows = skip
	}
}

// WithImportBatchSize 配置导入时批量处理的行数大小。
func WithImportBatchSize(size int) ImportOption {
	return func(c *importConfig) {
		if size > 0 {
			c.batchSize = size
		}
	}
}

// WithMaxImportRows 设置最大允许解析的数据行数限制（防御过大文件内存耗尽）。
func WithMaxImportRows(max int) ImportOption {
	return func(c *importConfig) {
		if max > 0 {
			c.maxRows = max
		}
	}
}

// WithMaxFileSize 设置最大允许上传/读取的文件物理字节数（默认 50MB）。
func WithMaxFileSize(limit int64) ImportOption {
	return func(c *importConfig) {
		if limit > 0 {
			c.maxFileSize = limit
		}
	}
}

// WithMemorySpoolThreshold 设置内存溢出到磁盘临时文件的字节阈值（默认 10MB）。
func WithMemorySpoolThreshold(threshold int64) ImportOption {
	return func(c *importConfig) {
		if threshold > 0 {
			c.memorySpoolThreshold = threshold
		}
	}
}

// WithUnzipSizeLimit 设置解压安全上限字节数（默认 256MB，防止 Zip Bomb 攻击）。
func WithUnzipSizeLimit(limit int64) ImportOption {
	return func(c *importConfig) {
		if limit > 0 {
			c.unzipSizeLimit = limit
		}
	}
}

// WithUnzipXMLSizeLimit 设置单个 XML 文件的解压上限字节数（默认 256MB）。
func WithUnzipXMLSizeLimit(limit int64) ImportOption {
	return func(c *importConfig) {
		if limit > 0 {
			c.unzipXMLSizeLimit = limit
		}
	}
}

// WithImportProgress 注册导入进度回调函数。
func WithImportProgress(fn func(processedRows int, totalRows int)) ImportOption {
	return func(c *importConfig) {
		c.onProgress = fn
	}
}

const defaultTempPath = ".cache/excel"

// Service 管理 Excel 与 CSV 的导入导出管线。
type Service struct {
	tempPath string
	db       *sql.DB
}

var (
	defaultMu      sync.RWMutex
	defaultService = NewService(defaultTempPath, nil)
)

// NewService 创建新的 Excel 服务实例。
func NewService(tempPath string, db *sql.DB) *Service {
	return &Service{
		tempPath: cmp.Or(tempPath, defaultTempPath),
		db:       db,
	}
}

// RegisterExcel 注册并配置 Excel 服务到全局。
func RegisterExcel(cfg *config.Config, db *sql.DB) (*Service, error) {
	service := NewService(lo.Ternary(cfg != nil, cfg.Excel.TempPath, ""), db)
	SetDefaultService(service)

	return service, nil
}

// SetDefaultService 注册全局默认的 Excel 服务实例。
func SetDefaultService(service *Service) {
	defaultMu.Lock()
	defer defaultMu.Unlock()

	defaultService = cmp.Or(service, NewService(defaultTempPath, nil))
}

// DefaultService 获取全局默认的 Excel 服务实例。
func DefaultService() *Service {
	defaultMu.RLock()
	defer defaultMu.RUnlock()

	return defaultService
}

// Export 是统一的全局导出入口，自动派发到具体的 Concern 实现。
func Export(ctx context.Context, export any, w io.Writer, opts ...ExportOption) error {
	return DefaultService().Export(ctx, export, w, opts...)
}

// Import 是统一的全局导入入口，自动派发到具体的 Concern 实现（100% 保持既有签名向后兼容）。
func Import(ctx context.Context, importObj any, r io.Reader, opts ...ImportOption) error {
	return DefaultService().Import(ctx, importObj, r, opts...)
}

// ImportWithResult 执行导入并返回完整的统计与校验失败信息列表。
func ImportWithResult(ctx context.Context, importObj any, r io.Reader, opts ...ImportOption) (*ImportResult, error) {
	return DefaultService().ImportWithResult(ctx, importObj, r, opts...)
}

// Export 在当前服务实例上执行导出操作。
func (s *Service) Export(ctx context.Context, export any, w io.Writer, opts ...ExportOption) error {
	if export == nil {
		return oops.
			Code(errs.CodeExcelNilPointer).
			In("excel.Export").
			Errorf("export object cannot be nil")
	}

	if w == nil {
		return oops.
			Code(errs.CodeExcelNilPointer).
			In("excel.Export").
			Errorf("writer cannot be nil")
	}

	cfg := newDefaultExportConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	if t, ok := export.(WithTitle); ok && t.Title() != "" {
		cfg.sheetName = t.Title()
	}

	return newExportPipeline(s, export, cfg).Write(ctx, w)
}

// Import 在当前服务实例上执行导入操作。
func (s *Service) Import(ctx context.Context, importObj any, r io.Reader, opts ...ImportOption) error {
	_, err := s.ImportWithResult(ctx, importObj, r, opts...)
	return err
}

// ImportWithResult 在当前服务实例上执行导入操作并返回详细统计报告。
func (s *Service) ImportWithResult(ctx context.Context, importObj any, r io.Reader, opts ...ImportOption) (*ImportResult, error) {
	if importObj == nil {
		return nil, oops.
			Code(errs.CodeExcelNilPointer).
			In("excel.Import").
			Errorf("importObj cannot be nil")
	}

	if r == nil {
		return nil, oops.
			Code(errs.CodeExcelNilPointer).
			In("excel.Import").
			Errorf("reader cannot be nil")
	}

	cfg := newDefaultImportConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	if t, ok := importObj.(WithTitle); ok && t.Title() != "" {
		cfg.sheetName = t.Title()
	}

	return newImportPipeline(s, importObj, cfg).Run(ctx, r)
}
