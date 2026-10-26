package excel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/xuri/excelize/v2"

	"cgin/pkg/errs"
)

const (
	// DefaultImportChunkSize 是流式导入的默认分块回调大小。
	DefaultImportChunkSize = 500
	// DefaultMaxImportBytes 是导入文件的默认最大字节数上限（20 MiB）。
	DefaultMaxImportBytes int64 = 20 << 20
	// maxValidationErrors 是单次导入最多收集的行级校验错误条数，防止恶意大文件撑爆错误响应体。
	maxValidationErrors = 50
)

type uniqueKeyExtractor[T any] struct {
	field   string
	extract func(row *T) string
}

type readConfig[T any] struct {
	sheet       string
	chunkSize   int
	maxBytes    int64
	uniqueKeys  []uniqueKeyExtractor[T]
	validateRow func(row *T) map[string][]string
}

// ReadOption 定义流式导入配置选项。
type ReadOption[T any] func(*readConfig[T])

// WithSheet 指定要读取的工作表名；为空时默认读取工作簿的第一个工作表。
func WithSheet[T any](sheet string) ReadOption[T] {
	return func(c *readConfig[T]) {
		c.sheet = strings.TrimSpace(sheet)
	}
}

// WithChunkSize 指定流式分块回调的批次行数（默认 500）。
func WithChunkSize[T any](size int) ReadOption[T] {
	return func(c *readConfig[T]) {
		if size > 0 {
			c.chunkSize = size
		}
	}
}

// WithMaxBytes 指定导入文件允许的最大字节数上限（默认 20 MiB）。
func WithMaxBytes[T any](maxBytes int64) ReadOption[T] {
	return func(c *readConfig[T]) {
		if maxBytes > 0 {
			c.maxBytes = maxBytes
		}
	}
}

// WithUniqueKey 注册文件内唯一键去重提取器，命中重复行时返回 EXCEL_DUPLICATE_ROW（409）。
func WithUniqueKey[T any](field string, extract func(row *T) string) ReadOption[T] {
	return func(c *readConfig[T]) {
		if field != "" && extract != nil {
			c.uniqueKeys = append(c.uniqueKeys, uniqueKeyExtractor[T]{
				field:   field,
				extract: extract,
			})
		}
	}
}

// WithRowValidator 注册单行跨列业务校验函数，返回字段名到错误文案列表的映射。
func WithRowValidator[T any](validate func(row *T) map[string][]string) ReadOption[T] {
	return func(c *readConfig[T]) {
		c.validateRow = validate
	}
}

// ReadTable 基于声明式 Column[T] 列契约流式读取并解析 xlsx，按分块大小回调 onBatch。
//
// 内存占用为 O(ChunkSize)：底层采用 excelize.Rows 流式游标逐行解析，不将整表载入内存。
func ReadTable[T any](
	ctx context.Context,
	r io.Reader,
	cols []Column[T],
	onBatch func(ctx context.Context, rows []T) error,
	opts ...ReadOption[T],
) error {
	if err := validateReadArgs(r, cols, onBatch); err != nil {
		return err
	}

	cfg := buildReadConfig(opts)
	file, err := openWorkbook(r, cfg.maxBytes)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	sheetName, err := resolveSheetName(file, cfg.sheet)
	if err != nil {
		return err
	}

	rows, err := file.Rows(sheetName)
	if err != nil {
		return errs.Wrap(errs.CodeExcelReadError, "Failed to read Excel worksheet", err).
			With("sheet", sheetName)
	}
	defer func() { _ = rows.Close() }()

	colIndexMap, colKeys, err := readAndMatchHeader(rows, cols)
	if err != nil {
		return err
	}

	state := newRowStreamState(cols, colIndexMap, colKeys, sheetName, &cfg)
	return state.streamAll(ctx, rows, onBatch)
}

func validateReadArgs[T any](
	r io.Reader,
	cols []Column[T],
	onBatch func(ctx context.Context, rows []T) error,
) error {
	if r == nil {
		return errors.New("excel: nil reader")
	}
	if onBatch == nil {
		return errors.New("excel: nil batch callback")
	}
	if len(cols) == 0 {
		return errors.New("excel: empty columns schema")
	}
	return nil
}

func buildReadConfig[T any](opts []ReadOption[T]) readConfig[T] {
	cfg := readConfig[T]{
		chunkSize: DefaultImportChunkSize,
		maxBytes:  DefaultMaxImportBytes,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

func openWorkbook(r io.Reader, maxBytes int64) (*excelize.File, error) {
	limReader := &maxBytesReader{r: r, max: maxBytes}
	file, err := excelize.OpenReader(limReader)
	if err != nil {
		if limReader.exceeded {
			return nil, errs.New(errs.CodeExcelFileTooLarge, "The uploaded Excel file exceeds the maximum allowed size").
				With("max_bytes", maxBytes)
		}
		return nil, errs.Wrap(errs.CodeExcelOpenError, "Failed to open Excel workbook", err)
	}
	return file, nil
}

func readAndMatchHeader[T any](rows *excelize.Rows, cols []Column[T]) ([]int, []string, error) {
	if !rows.Next() {
		if rErr := rows.Error(); rErr != nil {
			return nil, nil, errs.Wrap(errs.CodeExcelReadError, "Failed to read Excel header row", rErr)
		}
		return nil, nil, errs.New(errs.CodeEmptySheets, "The Excel worksheet is empty")
	}
	headerCells, err := rows.Columns()
	if err != nil {
		return nil, nil, errs.Wrap(errs.CodeExcelReadError, "Failed to read Excel header columns", err)
	}
	return matchHeaders(cols, headerCells)
}

type rowStreamState[T any] struct {
	cols        []Column[T]
	colIndexMap []int
	colKeys     []string
	sheetName   string
	cfg         *readConfig[T]
	seenKeys    []map[string]int
	batch       []T
	rowErr      *errs.Error
	errCount    int
	rowNum      int
}

func newRowStreamState[T any](
	cols []Column[T],
	colIndexMap []int,
	colKeys []string,
	sheetName string,
	cfg *readConfig[T],
) *rowStreamState[T] {
	seenKeys := make([]map[string]int, len(cfg.uniqueKeys))
	for i := range seenKeys {
		seenKeys[i] = make(map[string]int)
	}
	return &rowStreamState[T]{
		cols:        cols,
		colIndexMap: colIndexMap,
		colKeys:     colKeys,
		sheetName:   sheetName,
		cfg:         cfg,
		seenKeys:    seenKeys,
		batch:       make([]T, 0, cfg.chunkSize),
		rowNum:      1,
	}
}

func (s *rowStreamState[T]) appendFieldErr(path, msg string) {
	if s.rowErr == nil {
		s.rowErr = errs.New(errs.CodeExcelReadRowError, "One or more rows in the Excel file failed validation")
	}
	if s.errCount < maxValidationErrors {
		s.rowErr = s.rowErr.WithField(path, msg)
		s.errCount++
	}
}

func (s *rowStreamState[T]) streamAll(
	ctx context.Context,
	rows *excelize.Rows,
	onBatch func(ctx context.Context, rows []T) error,
) error {
	for rows.Next() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		s.rowNum++
		if s.rowNum > MaxRowsPerSheet {
			return errs.New(errs.CodeExcelRowsError, fmt.Sprintf("Excel worksheet exceeds maximum of %d rows", MaxRowsPerSheet)).
				With("sheet", s.sheetName).
				With("max_rows", MaxRowsPerSheet)
		}

		if err := s.processNextRow(ctx, rows, onBatch); err != nil {
			return err
		}
	}

	if rErr := rows.Error(); rErr != nil {
		return errs.Wrap(errs.CodeExcelReadError, "Error while streaming Excel rows", rErr)
	}
	if s.rowErr != nil {
		return s.rowErr
	}
	if len(s.batch) > 0 {
		return onBatch(ctx, s.batch)
	}
	return nil
}

func (s *rowStreamState[T]) processNextRow(
	ctx context.Context,
	rows *excelize.Rows,
	onBatch func(ctx context.Context, rows []T) error,
) error {
	cells, cErr := rows.Columns()
	if cErr != nil {
		return errs.Wrap(errs.CodeExcelReadError, fmt.Sprintf("Failed to read cells at row %d", s.rowNum), cErr).
			With("row", s.rowNum)
	}
	if isBlankRow(cells) {
		return nil
	}

	item, ok := s.decodeRow(cells)
	if !ok {
		return nil
	}

	if dupErr := s.checkDuplicates(&item); dupErr != nil {
		return dupErr
	}
	// 若前面已有校验失败行，继续扫描收集错误但不再触发写库回调
	if s.errCount > 0 {
		return nil
	}

	s.batch = append(s.batch, item)
	if len(s.batch) >= s.cfg.chunkSize {
		if bErr := onBatch(ctx, s.batch); bErr != nil {
			return bErr
		}
		s.batch = s.batch[:0]
	}
	return nil
}

func (s *rowStreamState[T]) decodeRow(cells []string) (T, bool) {
	var item T
	rowValid := true

	for colIdx, col := range s.cols {
		physIdx := s.colIndexMap[colIdx]
		var cellVal string
		if physIdx >= 0 && physIdx < len(cells) {
			cellVal = strings.TrimSpace(cells[physIdx])
		}

		fieldPath := fmt.Sprintf("rows.%d.%s", s.rowNum, s.colKeys[colIdx])
		if cellVal == "" {
			if col.Required {
				s.appendFieldErr(fieldPath, fmt.Sprintf("The %s field is required", col.Header))
				rowValid = false
			}
			continue
		}

		if col.Decode != nil {
			if dErr := col.Decode(cellVal, &item); dErr != nil {
				s.appendFieldErr(fieldPath, dErr.Error())
				rowValid = false
			}
		}
	}

	if s.cfg.validateRow != nil {
		for f, msgs := range s.cfg.validateRow(&item) {
			fieldPath := fmt.Sprintf("rows.%d.%s", s.rowNum, f)
			for _, m := range msgs {
				s.appendFieldErr(fieldPath, m)
				rowValid = false
			}
		}
	}

	return item, rowValid
}

func (s *rowStreamState[T]) checkDuplicates(item *T) error {
	for uIdx, uk := range s.cfg.uniqueKeys {
		val := strings.ToLower(strings.TrimSpace(uk.extract(item)))
		if val == "" {
			continue
		}
		if firstRow, exists := s.seenKeys[uIdx][val]; exists {
			fieldPath := fmt.Sprintf("rows.%d.%s", s.rowNum, uk.field)
			return errs.New(
				errs.CodeExcelDuplicateRow,
				fmt.Sprintf("Duplicate %s at row %d (conflicts with row %d)", uk.field, s.rowNum, firstRow),
			).
				WithField(fieldPath, fmt.Sprintf("Duplicate value (first seen at row %d)", firstRow)).
				With("field", uk.field).
				With("row", s.rowNum).
				With("first_row", firstRow)
		}
		s.seenKeys[uIdx][val] = s.rowNum
	}
	return nil
}

// resolveSheetName 确定目标工作表名：未指定时默认取第一个工作表；工作簿无工作表或指定表不存在时返回对应错误。
func resolveSheetName(file *excelize.File, requested string) (string, error) {
	sheets := file.GetSheetList()
	if len(sheets) == 0 {
		return "", errs.New(errs.CodeEmptySheets, "The Excel workbook contains no sheets")
	}
	if requested == "" {
		return sheets[0], nil
	}
	if !slices.Contains(sheets, requested) {
		return "", errs.New(errs.CodeEmptySheets, fmt.Sprintf("Worksheet %q was not found in the workbook", requested)).
			With("sheet", requested)
	}
	return requested, nil
}

// matchHeaders 大小写不敏感匹配表头列名，返回每个 Column[T] 对应的物理列下标（未出现则为 -1）及规范化字段名。
func matchHeaders[T any](cols []Column[T], headerCells []string) ([]int, []string, error) {
	if isBlankRow(headerCells) {
		return nil, nil, errs.New(errs.CodeEmptySheets, "The Excel header row is empty")
	}

	physByHeader := make(map[string]int, len(headerCells))
	for idx, raw := range headerCells {
		norm := strings.ToLower(strings.TrimSpace(raw))
		if norm != "" {
			if _, exists := physByHeader[norm]; !exists {
				physByHeader[norm] = idx
			}
		}
	}

	colIndexMap := make([]int, len(cols))
	colKeys := make([]string, len(cols))
	var mapErr *errs.Error

	for i, col := range cols {
		norm := strings.ToLower(strings.TrimSpace(col.Header))
		colKeys[i] = normalizeHeaderKey(col.Header)
		if physIdx, ok := physByHeader[norm]; ok {
			colIndexMap[i] = physIdx
		} else {
			colIndexMap[i] = -1
			if col.Required {
				if mapErr == nil {
					mapErr = errs.New(errs.CodeExcelMapError, "Missing required columns in Excel header")
				}
				mapErr = mapErr.WithField(colKeys[i], fmt.Sprintf("Missing required column %q", col.Header))
			}
		}
	}

	if mapErr != nil {
		return nil, nil, mapErr
	}
	return colIndexMap, colKeys, nil
}

// normalizeHeaderKey 将表头名（如 "Created At"）转换为小驼峰/下划线友好的字段标识（如 "created_at"）。
func normalizeHeaderKey(header string) string {
	trimmed := strings.ToLower(strings.TrimSpace(header))
	return strings.Join(strings.Fields(trimmed), "_")
}

// isBlankRow 判断整行单元格是否全部为空白。
func isBlankRow(cells []string) bool {
	for _, c := range cells {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// maxBytesReader 限制读取的最大字节数，超出时标记 exceeded 并中止读取。
type maxBytesReader struct {
	r        io.Reader
	max      int64
	read     int64
	exceeded bool
}

func (m *maxBytesReader) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	m.read += int64(n)
	if m.max > 0 && m.read > m.max {
		m.exceeded = true
		return n, errors.New("excel: file size exceeds maximum allowed bytes")
	}
	return n, err
}
