package excel

import (
	"cmp"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"

	"github.com/samber/oops"
	"github.com/xuri/excelize/v2"

	"bgin/internal/platform/errs"
)

// StreamImportOption 定义流式导入配置选项别名，与 ImportOption 保持统一。
type StreamImportOption = ImportOption

// WithStreamSheetName 指定读取的工作表名称。
func WithStreamSheetName(name string) StreamImportOption {
	return WithImportSheetName(name)
}

// WithHeaderRowIndex 配置跳过的表头行数（从 1 开始计数）。
func WithHeaderRowIndex(idx int) StreamImportOption {
	return WithHeadingRowIndex(idx)
}

// sliceGenerator 构造基于泛型切片的流式迭代器生成器。
func sliceGenerator[T any](items []T) func(yield func(item T) bool) error {
	return func(yield func(item T) bool) error {
		for _, item := range items {
			if !yield(item) {
				break
			}
		}
		return nil
	}
}

// ExportSlice 使用流式写入器将内存中的泛型切片导出为 Excel 格式。
func ExportSlice[T any](
	ctx context.Context,
	w io.Writer,
	items []T,
	headings []string,
	mapper func(item T) []any,
	opts ...ExportOption,
) error {
	return StreamExport(ctx, w, headings, sliceGenerator(items), mapper, opts...)
}

// ExportSliceCSV 以 O(1) 内存将内存中的泛型切片导出为 CSV 格式。
func ExportSliceCSV[T any](
	ctx context.Context,
	w io.Writer,
	items []T,
	headings []string,
	mapper func(item T) []any,
) error {
	return StreamExportCSV(ctx, w, headings, sliceGenerator(items), mapper)
}

// ExportIterator 使用序列生成器函数将元素直接导出到 Excel 写入器。
func ExportIterator[T any](
	ctx context.Context,
	w io.Writer,
	seq func(yield func(T) bool),
	headings []string,
	mapper func(item T) []any,
	opts ...ExportOption,
) error {
	return StreamExport(ctx, w, headings, func(yield func(item T) bool) error {
		seq(yield)
		return nil
	}, mapper, opts...)
}

// ExportBuilder 提供基于泛型的流式导出链式建造者。
type ExportBuilder[T any] struct {
	items     []T
	seq       func(yield func(T) bool)
	generator func(yield func(item T) bool) error
	headings  []string
	mapper    func(item T) []any
	opts      []ExportOption
}

// NewExportBuilder 创建针对类型 T 的流式导出建造者。
func NewExportBuilder[T any]() *ExportBuilder[T] {
	return &ExportBuilder[T]{}
}

// FromSlice 设置导出数据源为切片。
func (b *ExportBuilder[T]) FromSlice(items []T) *ExportBuilder[T] {
	b.items = items
	return b
}

// FromSeq 设置导出数据源为流式生成器/迭代序列。
func (b *ExportBuilder[T]) FromSeq(seq func(yield func(T) bool)) *ExportBuilder[T] {
	b.seq = seq
	return b
}

// FromGenerator 设置导出数据源为流式生成器函数。
func (b *ExportBuilder[T]) FromGenerator(gen func(yield func(item T) bool) error) *ExportBuilder[T] {
	b.generator = gen
	return b
}

// WithHeadings 设置表头列名。
func (b *ExportBuilder[T]) WithHeadings(headings ...string) *ExportBuilder[T] {
	b.headings = headings
	return b
}

// WithMapping 设置单行数据映射规则。
func (b *ExportBuilder[T]) WithMapping(mapper func(item T) []any) *ExportBuilder[T] {
	b.mapper = mapper
	return b
}

// WithOptions 附加函数式配置项。
func (b *ExportBuilder[T]) WithOptions(opts ...ExportOption) *ExportBuilder[T] {
	b.opts = append(b.opts, opts...)
	return b
}

// Write 执行 Excel 流式导出并写入目标输出端。
func (b *ExportBuilder[T]) Write(ctx context.Context, w io.Writer) error {
	if b.generator != nil {
		return StreamExport(ctx, w, b.headings, b.generator, b.mapper, b.opts...)
	}
	if b.seq != nil {
		return ExportIterator(ctx, w, b.seq, b.headings, b.mapper, b.opts...)
	}
	return ExportSlice(ctx, w, b.items, b.headings, b.mapper, b.opts...)
}

// WriteCSV 执行 CSV 流式导出并写入目标输出端。
func (b *ExportBuilder[T]) WriteCSV(ctx context.Context, w io.Writer) error {
	if b.generator != nil {
		return StreamExportCSV(ctx, w, b.headings, b.generator, b.mapper)
	}
	if b.seq != nil {
		return StreamExportCSV(ctx, w, b.headings, func(yield func(item T) bool) error {
			b.seq(yield)
			return nil
		}, b.mapper)
	}
	return ExportSliceCSV(ctx, w, b.items, b.headings, b.mapper)
}

func initStreamWriter(
	file *excelize.File,
	sheetName string,
	headings []string,
	cfg *exportConfig,
) (*excelize.StreamWriter, int, error) {
	sheetName = cmp.Or(sheetName, defaultSheetName)
	ensureSheet(file, sheetName)

	if cfg != nil && cfg.freezePanes {
		_ = file.SetPanes(sheetName, &excelize.Panes{
			Freeze:      true,
			Split:       false,
			XSplit:      0,
			YSplit:      1,
			TopLeftCell: "A2",
			ActivePane:  "bottomLeft",
		})
	}

	var headerStyleID int
	if cfg != nil && cfg.headerStyle != nil {
		headerStyleID, _ = file.NewStyle(cfg.headerStyle)
	}

	sw, err := file.NewStreamWriter(sheetName)
	if err != nil {
		return nil, 0, oops.Code(errs.CodeExcelStreamError).Wrap(err)
	}

	rowIdx := 1
	if len(headings) > 0 {
		if err := writeStreamHeaderRow(sw, headings, headerStyleID); err != nil {
			return nil, 0, err
		}
		rowIdx++
	}

	return sw, rowIdx, nil
}

func writeStreamHeaderRow(sw *excelize.StreamWriter, headings []string, headerStyleID int) error {
	cells := make([]any, len(headings))
	for i, h := range headings {
		if headerStyleID > 0 {
			cells[i] = excelize.Cell{Value: h, StyleID: headerStyleID}
		} else {
			cells[i] = excelize.Cell{Value: h}
		}
	}

	cellName, cellErr := excelize.CoordinatesToCellName(1, 1)
	if cellErr != nil {
		return oops.Code(errs.CodeExcelStreamError).Wrap(cellErr)
	}

	if err := sw.SetRow(cellName, cells); err != nil {
		return oops.Code(errs.CodeExcelStreamError).Wrap(err)
	}

	return nil
}

func writeStreamRow[T any](
	sw *excelize.StreamWriter,
	item T,
	rowIdx int,
	mapper func(item T) []any,
	cfg *exportConfig,
	colStyleIDs map[int]int,
	widths map[int]int,
	sampleWidth bool,
) error {
	var rowValues []any
	if mapper != nil {
		rowValues = mapper(item)
	} else {
		rowValues = []any{item}
	}

	sanitize := true
	if cfg != nil {
		sanitize = cfg.sanitizeFormulas
	}

	cells := make([]any, len(rowValues))
	for i, v := range rowValues {
		formatted := formatValueWithOptions(v, sanitize)
		if sID, hasStyle := colStyleIDs[i]; hasStyle {
			cells[i] = excelize.Cell{Value: formatted, StyleID: sID}
		} else {
			cells[i] = excelize.Cell{Value: formatted}
		}

		if sampleWidth {
			cellLen := len([]rune(fmt.Sprintf("%v", formatted)))
			if current, exists := widths[i]; !exists || cellLen > current {
				widths[i] = cellLen
			}
		}
	}

	cellName, err := excelize.CoordinatesToCellName(1, rowIdx)
	if err != nil {
		return err
	}

	return sw.SetRow(cellName, cells)
}

// StreamExport 使用生成器迭代函数将元素流式导出到 Excel 写入器，内存占用保持 O(1)。
func StreamExport[T any](
	ctx context.Context,
	w io.Writer,
	headings []string,
	generator func(yield func(item T) bool) error,
	mapper func(item T) []any,
	opts ...ExportOption,
) error {
	if w == nil {
		return oops.Code(errs.CodeNilWriter).Errorf("writer cannot be nil")
	}
	if generator == nil {
		return oops.Code(errs.CodeNilGenerator).Errorf("generator cannot be nil")
	}

	cfg := newDefaultExportConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	file := excelize.NewFile()
	defer func() { _ = file.Close() }()

	sw, rowIdx, err := initStreamWriter(file, cfg.sheetName, headings, cfg)
	if err != nil {
		return err
	}

	colStyleIDs := make(map[int]int)
	if cfg.columnFormats != nil {
		for colName, fmtStr := range cfg.columnFormats {
			colNum, err := excelize.ColumnNameToNumber(colName)
			if err == nil {
				customFmt := fmtStr
				sID, err := file.NewStyle(&excelize.Style{CustomNumFmt: &customFmt})
				if err == nil {
					colStyleIDs[colNum-1] = sID
				}
			}
		}
	}

	widths := make(map[int]int)
	for i, h := range headings {
		widths[i] = len([]rune(h))
	}

	var yieldErr error
	totalRows := 0

	genErr := generator(func(item T) bool {
		select {
		case <-ctx.Done():
			yieldErr = ctx.Err()
			return false
		default:
		}

		sampleWidth := totalRows < widthSampleLimit
		if err := writeStreamRow(sw, item, rowIdx, mapper, cfg, colStyleIDs, widths, sampleWidth); err != nil {
			yieldErr = err
			return false
		}
		rowIdx++
		totalRows++
		return true
	})
	if genErr != nil {
		return oops.Code(errs.CodeExcelGeneratorError).Wrap(genErr)
	}
	if yieldErr != nil {
		if errors.Is(yieldErr, context.Canceled) || errors.Is(yieldErr, context.DeadlineExceeded) {
			return yieldErr
		}
		return oops.Code(errs.CodeExcelStreamWriteError).Wrap(yieldErr)
	}

	if err := sw.Flush(); err != nil {
		return oops.Code(errs.CodeExcelFlushError).Wrap(err)
	}

	applyStreamColumnWidthsDirect(file, cfg.sheetName, widths, cfg)

	return file.Write(w)
}

func applyStreamColumnWidthsDirect(
	f *excelize.File,
	sheetName string,
	widths map[int]int,
	cfg *exportConfig,
) {
	if cfg != nil && cfg.columnWidths != nil {
		for colStr, width := range cfg.columnWidths {
			_ = f.SetColWidth(sheetName, colStr, colStr, width)
		}
		return
	}

	shouldAuto := false
	if cfg != nil && cfg.autoWidth != nil {
		shouldAuto = *cfg.autoWidth
	}

	if shouldAuto {
		for colIdx, width := range widths {
			colName, err := excelize.ColumnNumberToName(colIdx + 1)
			if err != nil {
				continue
			}

			computedWidth := math.Max(float64(width+3), 10.0)
			_ = f.SetColWidth(sheetName, colName, colName, computedWidth)
		}
	}
}

// StreamExportCSV 使用生成器迭代函数将元素直接导出为 CSV 格式，支持百万级数据高效导出并保持 O(1) 内存。
func StreamExportCSV[T any](
	ctx context.Context,
	w io.Writer,
	headings []string,
	generator func(yield func(item T) bool) error,
	mapper func(item T) []any,
) error {
	if w == nil {
		return oops.Code(errs.CodeNilWriter).Errorf("writer cannot be nil")
	}
	if generator == nil {
		return oops.Code(errs.CodeNilGenerator).Errorf("generator cannot be nil")
	}

	cw := csv.NewWriter(w)

	if len(headings) > 0 {
		if err := cw.Write(headings); err != nil {
			return oops.Code(errs.CodeCSVWriteError).Wrap(err)
		}
	}

	var yieldErr error

	genErr := generator(func(item T) bool {
		select {
		case <-ctx.Done():
			yieldErr = ctx.Err()
			return false
		default:
		}

		var rowValues []any
		if mapper != nil {
			rowValues = mapper(item)
		} else {
			rowValues = []any{item}
		}

		record := make([]string, len(rowValues))
		for i, v := range rowValues {
			record[i] = fmt.Sprintf("%v", formatValueWithOptions(v, true))
		}

		if err := cw.Write(record); err != nil {
			yieldErr = err
			return false
		}
		return true
	})
	if genErr != nil {
		return oops.Code(errs.CodeCSVGeneratorError).Wrap(genErr)
	}
	if yieldErr != nil {
		if errors.Is(yieldErr, context.Canceled) || errors.Is(yieldErr, context.DeadlineExceeded) {
			return yieldErr
		}
		return oops.Code(errs.CodeCSVWriteError).Wrap(yieldErr)
	}

	cw.Flush()
	if err := cw.Error(); err != nil {
		return oops.Code(errs.CodeCSVFlushError).Wrap(err)
	}

	return nil
}

func resolveSheetName(file *excelize.File, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	sheets := file.GetSheetList()
	if len(sheets) == 0 {
		return "", oops.Code(errs.CodeEmptySheets).Errorf("no sheets found in excel workbook")
	}
	return sheets[0], nil
}

func processImportRow[T any](
	rowColumns []string,
	rowIdx int,
	mapper func(row []string, rowIdx int) (T, error),
	consumer func(item T, rowIdx int) error,
	skipInvalid bool,
) (bool, error) {
	if len(rowColumns) == 0 {
		return false, nil
	}

	item, mapErr := mapper(rowColumns, rowIdx)
	if mapErr != nil {
		if skipInvalid {
			return false, nil
		}
		return false, oops.Code(errs.CodeExcelMapError).With("row_idx", rowIdx).Wrap(mapErr)
	}

	if err := consumer(item, rowIdx); err != nil {
		return false, oops.Code(errs.CodeExcelConsumeError).With("row_idx", rowIdx).Wrap(err)
	}

	return true, nil
}

func iterateRows[T any](
	ctx context.Context,
	rows *excelize.Rows,
	headerRowIndex int,
	skipInvalid bool,
	mapper func(row []string, rowIdx int) (T, error),
	consumer func(item T, rowIdx int) error,
) (int, error) {
	currentRowIdx, processedCount := 0, 0
	for rows.Next() {
		currentRowIdx++
		select {
		case <-ctx.Done():
			return processedCount, oops.Code(errs.CodeContextCanceled).With("row_idx", currentRowIdx).Wrap(ctx.Err())
		default:
		}

		if currentRowIdx <= headerRowIndex {
			continue
		}

		rowCols, colErr := rows.Columns()
		if colErr != nil {
			if skipInvalid {
				continue
			}
			return processedCount, oops.Code(errs.CodeExcelReadRowError).With("row_idx", currentRowIdx).Wrap(colErr)
		}

		processed, procErr := processImportRow(rowCols, currentRowIdx, mapper, consumer, skipInvalid)
		if procErr != nil {
			return processedCount, procErr
		}
		if processed {
			processedCount++
		}
	}
	return processedCount, nil
}

// StreamImport 从 Excel 读取器按行顺序流式读取，保持低内存开销。
func StreamImport[T any](
	ctx context.Context,
	r io.Reader,
	mapper func(row []string, rowIdx int) (T, error),
	consumer func(item T, rowIdx int) error,
	opts ...StreamImportOption,
) (int, error) {
	if r == nil {
		return 0, oops.Code(errs.CodeNilReader).Errorf("reader cannot be nil")
	}
	if mapper == nil || consumer == nil {
		return 0, oops.Code(errs.CodeNilCallback).Errorf("mapper and consumer cannot be nil")
	}

	cfg := newDefaultImportConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	file, err := excelize.OpenReader(r)
	if err != nil {
		return 0, oops.Code(errs.CodeExcelOpenError).Wrap(err)
	}
	defer func() { _ = file.Close() }()

	sheetName, err := resolveSheetName(file, cfg.sheetName)
	if err != nil {
		return 0, err
	}

	rows, err := file.Rows(sheetName)
	if err != nil {
		return 0, oops.Code(errs.CodeExcelRowsError).Wrap(err)
	}
	defer func() { _ = rows.Close() }()

	return iterateRows(ctx, rows, cfg.headerRowIndex, cfg.skipInvalidRows, mapper, consumer)
}

// StreamImportCSV 从 CSV 读取器按行顺序流式读取，具备真正的 O(1) 内存特性。
func StreamImportCSV[T any](
	ctx context.Context,
	r io.Reader,
	mapper func(row []string, rowIdx int) (T, error),
	consumer func(item T, rowIdx int) error,
	opts ...StreamImportOption,
) (int, error) {
	if r == nil {
		return 0, oops.Code(errs.CodeNilReader).Errorf("reader cannot be nil")
	}
	if mapper == nil || consumer == nil {
		return 0, oops.Code(errs.CodeNilCallback).Errorf("mapper and consumer cannot be nil")
	}

	cfg := newDefaultImportConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	cr := csv.NewReader(r)
	currentRowIdx, processedCount := 0, 0

	for {
		select {
		case <-ctx.Done():
			return processedCount, oops.Code(errs.CodeContextCanceled).Wrap(ctx.Err())
		default:
		}

		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			if cfg.skipInvalidRows {
				continue
			}
			return processedCount, oops.Code(errs.CodeCSVReadError).With("row_idx", currentRowIdx).Wrap(err)
		}

		currentRowIdx++
		if currentRowIdx <= cfg.headerRowIndex {
			continue
		}

		processed, procErr := processImportRow(record, currentRowIdx, mapper, consumer, cfg.skipInvalidRows)
		if procErr != nil {
			return processedCount, procErr
		}
		if processed {
			processedCount++
		}
	}

	return processedCount, nil
}

// ImportToModels 将 Excel 内容按行通过自定义 mapper 函数映射并解析为领域模型切片，同时收集详细统计报告。
func ImportToModels[T any](
	ctx context.Context,
	r io.Reader,
	mapper func(row []string, rowIdx int) (T, error),
	opts ...ImportOption,
) ([]T, *ImportResult, error) {
	if r == nil {
		return nil, nil, oops.Code(errs.CodeExcelNilPointer).In("excel.ImportToModels").Errorf("reader cannot be nil")
	}
	if mapper == nil {
		return nil, nil, oops.Code(errs.CodeExcelNilPointer).In("excel.ImportToModels").Errorf("mapper cannot be nil")
	}

	cfg := newDefaultImportConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	file, err := excelize.OpenReader(r)
	if err != nil {
		return nil, nil, oops.Code(errs.CodeExcelOpenError).Wrap(err)
	}
	defer func() { _ = file.Close() }()

	sheetName, err := resolveSheetName(file, cfg.sheetName)
	if err != nil {
		return nil, nil, err
	}

	rows, err := file.Rows(sheetName)
	if err != nil {
		return nil, nil, oops.Code(errs.CodeExcelRowsError).Wrap(err)
	}
	defer func() { _ = rows.Close() }()

	result := &ImportResult{}
	var items []T
	currentRowIdx := 0

	for rows.Next() {
		currentRowIdx++
		select {
		case <-ctx.Done():
			return items, result, ctx.Err()
		default:
		}

		if currentRowIdx <= cfg.headerRowIndex {
			continue
		}

		cols, colErr := rows.Columns()
		if colErr != nil {
			if cfg.skipInvalidRows {
				result.SkippedCount++
				result.Failures = append(result.Failures, RowFailure{
					Row:   currentRowIdx,
					Error: colErr,
				})
				continue
			}
			return items, result, oops.Code(errs.CodeExcelReadRowError).With("row_idx", currentRowIdx).Wrap(colErr)
		}

		if len(cols) == 0 || isRowEmpty(cols) {
			continue
		}

		if cfg.maxRows > 0 && result.TotalRows >= cfg.maxRows {
			break
		}

		result.TotalRows++

		item, mapErr := mapper(cols, currentRowIdx)
		if mapErr != nil {
			if cfg.skipInvalidRows {
				result.SkippedCount++
				result.Failures = append(result.Failures, RowFailure{
					Row:    currentRowIdx,
					Values: cols,
					Error:  mapErr,
				})
				continue
			}
			return items, result, oops.Code(errs.CodeExcelMapError).With("row_idx", currentRowIdx).Wrap(mapErr)
		}

		items = append(items, item)
		result.ImportedCount++
	}

	return items, result, nil
}

func checkRowHeaderOrSkip(cols []string, headerRowIdx, currentRowIdx int) (skip bool, isHeader bool) {
	if len(cols) == 0 || isRowEmpty(cols) {
		return true, false
	}
	if headerRowIdx > 0 && currentRowIdx < headerRowIdx {
		return true, false
	}
	if headerRowIdx > 0 && currentRowIdx == headerRowIdx {
		return false, true
	}
	return false, false
}

func decodeElement[T any](cols, headers []string, isPtr bool, baseType reflect.Type) (T, error) {
	newElem := reflect.New(baseType)
	if err := mapRowToStruct(cols, headers, newElem.Elem()); err != nil {
		var zero T
		return zero, err
	}

	var item T
	if isPtr {
		val, ok := reflect.TypeAssert[T](newElem)
		if ok {
			item = val
		}
	} else {
		val, ok := reflect.TypeAssert[T](newElem.Elem())
		if ok {
			item = val
		}
	}

	return item, nil
}

// ImportSlice 将 Excel 单元格流直接反序列化为泛型结构体切片，避免外部构造繁重的 Concern 结构体。
func ImportSlice[T any](
	ctx context.Context,
	r io.Reader,
	dest *[]T,
	opts ...ImportOption,
) error {
	if r == nil {
		return oops.Code(errs.CodeExcelNilPointer).In("excel.ImportSlice").Errorf("reader cannot be nil")
	}
	if dest == nil {
		return oops.Code(errs.CodeExcelNilPointer).In("excel.ImportSlice").Errorf("destination slice pointer cannot be nil")
	}

	cfg := newDefaultImportConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	file, err := excelize.OpenReader(r)
	if err != nil {
		return oops.Code(errs.CodeExcelOpenError).Wrap(err)
	}
	defer func() { _ = file.Close() }()

	sheetName, err := resolveSheetName(file, cfg.sheetName)
	if err != nil {
		return err
	}

	rows, err := file.Rows(sheetName)
	if err != nil {
		return oops.Code(errs.CodeExcelRowsError).Wrap(err)
	}
	defer func() { _ = rows.Close() }()

	elemType := reflect.TypeFor[T]()
	isPtr := elemType.Kind() == reflect.Pointer
	baseType := elemType
	if isPtr {
		baseType = elemType.Elem()
	}

	var headers []string
	var items []T
	currentRowIdx := 0

	for rows.Next() {
		currentRowIdx++
		cols, colErr := rows.Columns()
		if colErr != nil {
			if cfg.skipInvalidRows {
				continue
			}
			return oops.Code(errs.CodeExcelReadRowError).With("row_idx", currentRowIdx).Wrap(colErr)
		}

		skip, isHeader := checkRowHeaderOrSkip(cols, cfg.headerRowIndex, currentRowIdx)
		if isHeader {
			headers = cols
			continue
		}
		if skip {
			continue
		}

		item, mapErr := decodeElement[T](cols, headers, isPtr, baseType)
		if mapErr != nil {
			if cfg.skipInvalidRows {
				continue
			}
			return oops.Code(errs.CodeExcelMapError).With("row_idx", currentRowIdx).Wrap(mapErr)
		}

		items = append(items, item)
	}

	*dest = items
	return nil
}
