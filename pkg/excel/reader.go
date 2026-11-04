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

type uniqueKeyExtractor[T any] struct {
	field   string
	extract func(row *T) string
}

type readConfig[T any] struct {
	sheet       string
	chunkSize   int
	maxBytes    int64
	skipInvalid bool
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

// WithSkipInvalid 设置是否跳过不合法的行并继续导入剩余有效行（默认 false：全表预检不入库）。
func WithSkipInvalid[T any](skip bool) ReadOption[T] {
	return func(c *readConfig[T]) {
		c.skipInvalid = skip
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

// Reader 提供基于泛型列契约的 xlsx 游标流式解析器。
type Reader[T any] struct {
	cols []ImportColumn[T]
	cfg  readConfig[T]
}

// NewReader 构造流式读取解析器。
func NewReader[T any](cols []ImportColumn[T], opts ...ReadOption[T]) *Reader[T] {
	cfg := readConfig[T]{
		chunkSize: DefaultChunkSize,
		maxBytes:  DefaultMaxImportBytes,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	return &Reader[T]{
		cols: cols,
		cfg:  cfg,
	}
}

type rowStreamState[T any] struct {
	reader      *Reader[T]
	colIndexMap []int
	colKeys     []string
	sheetName   string
	seenKeys    []map[string]int
	batch       []T
	rowErr      *errs.Error
	result      ImportResult
	rowNum      int
}

func newRowStreamState[T any](
	r *Reader[T],
	sheetName string,
	colIndexMap []int,
	colKeys []string,
) *rowStreamState[T] {
	seenKeys := make([]map[string]int, len(r.cfg.uniqueKeys))
	for i := range seenKeys {
		seenKeys[i] = make(map[string]int)
	}

	return &rowStreamState[T]{
		reader:      r,
		colIndexMap: colIndexMap,
		colKeys:     colKeys,
		sheetName:   sheetName,
		seenKeys:    seenKeys,
		batch:       make([]T, 0, r.cfg.chunkSize),
		rowNum:      1,
	}
}

func (s *rowStreamState[T]) recordRowErrors(errors []RowError) {
	if len(errors) == 0 {
		return
	}
	s.result.FailedRows++
	if s.rowErr == nil {
		s.rowErr = errs.New(errs.CodeExcelReadRowError, "One or more rows in the Excel file failed validation")
	}
	for _, re := range errors {
		if len(s.result.Errors) < MaxValidationErrors {
			s.result.Errors = append(s.result.Errors, re)
		}
		if len(s.rowErr.Fields()) < MaxValidationErrors {
			fieldPath := fmt.Sprintf("rows.%d.%s", re.Row, re.Field)
			s.rowErr = s.rowErr.WithField(fieldPath, re.Message)
		}
	}
}

func (s *rowStreamState[T]) validateCustomRules(item *T) bool {
	if s.reader.cfg.validateRow == nil {
		return true
	}
	valErrs := s.reader.cfg.validateRow(item)
	if len(valErrs) == 0 {
		return true
	}

	rowErrors := make([]RowError, 0, len(valErrs))
	for f, msgs := range valErrs {
		for _, m := range msgs {
			rowErrors = append(rowErrors, RowError{
				Row:     s.rowNum,
				Field:   f,
				Header:  f,
				Message: m,
			})
		}
	}
	s.recordRowErrors(rowErrors)
	return false
}

func (s *rowStreamState[T]) checkDuplicates(item *T) error {
	for uIdx, uk := range s.reader.cfg.uniqueKeys {
		val := strings.ToLower(strings.TrimSpace(uk.extract(item)))
		if val == "" {
			continue
		}
		if firstRow, exists := s.seenKeys[uIdx][val]; exists {
			if s.reader.cfg.skipInvalid {
				s.recordRowErrors([]RowError{
					{
						Row:     s.rowNum,
						Field:   uk.field,
						Header:  uk.field,
						Message: fmt.Sprintf("Duplicate %s (conflicts with row %d)", uk.field, firstRow),
					},
				})
				return nil
			}
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

func (s *rowStreamState[T]) processRow(
	ctx context.Context,
	cells []string,
	onBatch func(ctx context.Context, batch []T) error,
) error {
	s.result.TotalRows++

	item, rowErrors := s.reader.decodeRow(s.rowNum, cells, s.colIndexMap, s.colKeys)
	if len(rowErrors) > 0 {
		s.recordRowErrors(rowErrors)
		return nil
	}

	if !s.validateCustomRules(&item) {
		return nil
	}

	failedBefore := s.result.FailedRows
	if dupErr := s.checkDuplicates(&item); dupErr != nil {
		return dupErr
	}
	if s.result.FailedRows > failedBefore {
		return nil
	}

	if s.result.FailedRows > 0 && !s.reader.cfg.skipInvalid {
		return nil
	}

	s.result.SuccessRows++
	s.batch = append(s.batch, item)
	if len(s.batch) >= s.reader.cfg.chunkSize {
		if bErr := onBatch(ctx, s.batch); bErr != nil {
			return bErr
		}
		s.batch = s.batch[:0]
	}
	return nil
}

// Read 驱动输入流，逐行流式解析、执行唯一性去重与行级校验，分块回调 onBatch，并返回汇总报告。
func (r *Reader[T]) Read(
	ctx context.Context,
	reader io.Reader,
	onBatch func(ctx context.Context, batch []T) error,
) (ImportResult, error) {
	if reader == nil {
		return ImportResult{}, errors.New("excel: nil reader")
	}
	if onBatch == nil {
		return ImportResult{}, errors.New("excel: nil batch callback")
	}
	if len(r.cols) == 0 {
		return ImportResult{}, errors.New("excel: empty columns schema")
	}

	file, err := openWorkbook(reader, r.cfg.maxBytes)
	if err != nil {
		return ImportResult{}, err
	}
	defer func() { _ = file.Close() }()

	sheetName, err := resolveSheetName(file, r.cfg.sheet)
	if err != nil {
		return ImportResult{}, err
	}

	rows, err := file.Rows(sheetName)
	if err != nil {
		return ImportResult{}, errs.Wrap(errs.CodeExcelReadError, "Failed to read Excel worksheet", err).
			With("sheet", sheetName)
	}
	defer func() { _ = rows.Close() }()

	colIndexMap, colKeys, err := readAndMatchHeader(rows, r.cols)
	if err != nil {
		return ImportResult{}, err
	}

	state := newRowStreamState(r, sheetName, colIndexMap, colKeys)
	return state.streamAll(ctx, rows, onBatch)
}

func (s *rowStreamState[T]) streamAll(
	ctx context.Context,
	rows *excelize.Rows,
	onBatch func(ctx context.Context, batch []T) error,
) (ImportResult, error) {
	for rows.Next() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return s.result, ctxErr
		}
		s.rowNum++
		if s.rowNum > MaxRowsPerSheet {
			return s.result, errs.New(errs.CodeExcelRowsError, fmt.Sprintf("Excel worksheet exceeds maximum of %d rows", MaxRowsPerSheet)).
				With("sheet", s.sheetName).
				With("max_rows", MaxRowsPerSheet)
		}

		cells, cErr := rows.Columns()
		if cErr != nil {
			return s.result, errs.Wrap(errs.CodeExcelReadError, fmt.Sprintf("Failed to read cells at row %d", s.rowNum), cErr).
				With("row", s.rowNum)
		}
		if isBlankRow(cells) {
			continue
		}

		if err := s.processRow(ctx, cells, onBatch); err != nil {
			return s.result, err
		}
	}

	if rErr := rows.Error(); rErr != nil {
		return s.result, errs.Wrap(errs.CodeExcelReadError, "Error while streaming Excel rows", rErr)
	}

	if s.result.FailedRows > 0 && !s.reader.cfg.skipInvalid {
		return s.result, s.rowErr
	}

	if len(s.batch) > 0 {
		if bErr := onBatch(ctx, s.batch); bErr != nil {
			return s.result, bErr
		}
	}

	return s.result, nil
}

func (r *Reader[T]) decodeRow(
	rowNum int,
	cells []string,
	colIndexMap []int,
	colKeys []string,
) (T, []RowError) {
	var item T
	var errors []RowError

	for colIdx, col := range r.cols {
		physIdx := colIndexMap[colIdx]
		var cellVal string
		if physIdx >= 0 && physIdx < len(cells) {
			cellVal = strings.TrimSpace(cells[physIdx])
		}

		if cellVal == "" {
			switch {
			case col.Default != "":
				cellVal = col.Default
			case col.Required:
				errors = append(errors, RowError{
					Row:     rowNum,
					Field:   colKeys[colIdx],
					Header:  col.Header,
					Message: fmt.Sprintf("The %s field is required", col.Header),
				})
				continue
			default:
				continue
			}
		}

		if col.Decode != nil {
			if dErr := col.Decode(cellVal, &item); dErr != nil {
				errors = append(errors, RowError{
					Row:     rowNum,
					Field:   colKeys[colIdx],
					Header:  col.Header,
					Message: dErr.Error(),
				})
			}
		}
	}

	return item, errors
}

func openWorkbook(reader io.Reader, maxBytes int64) (*excelize.File, error) {
	if maxBytes > 0 && exceedsKnownSize(reader, maxBytes) {
		return nil, errs.New(errs.CodeExcelFileTooLarge, "The uploaded Excel file exceeds the maximum allowed size").
			With("max_bytes", maxBytes)
	}

	limReader := &maxBytesReader{r: reader, max: maxBytes}
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

func exceedsKnownSize(reader io.Reader, maxBytes int64) bool {
	if sizer, ok := reader.(interface{ Size() int64 }); ok {
		return sizer.Size() > maxBytes
	}
	if lener, ok := reader.(interface{ Len() int }); ok {
		return int64(lener.Len()) > maxBytes
	}
	return false
}

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

func readAndMatchHeader[T any](rows *excelize.Rows, cols []ImportColumn[T]) ([]int, []string, error) {
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

func matchHeaders[T any](cols []ImportColumn[T], headerCells []string) ([]int, []string, error) {
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

func normalizeHeaderKey(header string) string {
	trimmed := strings.ToLower(strings.TrimSpace(header))
	return strings.Join(strings.Fields(trimmed), "_")
}

func isBlankRow(cells []string) bool {
	for _, c := range cells {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

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
