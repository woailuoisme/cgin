package excel

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"

	"github.com/samber/lo"
	"github.com/samber/oops"
	"github.com/xuri/excelize/v2"

	"bgin/internal/platform/errs"
)

const (
	defaultSheetName       = "Sheet1"
	defaultMaxRowsPerSheet = 1000000 // Excel 规范单工作表最大行数上限为 1,048,576
	widthSampleLimit       = 500     // 自动列宽抽样前 500 行以平衡性能与排版
)

type exportPipeline struct {
	service *Service
	export  any
	cfg     *exportConfig
}

func newExportPipeline(service *Service, export any, cfg *exportConfig) *exportPipeline {
	return &exportPipeline{
		service: service,
		export:  export,
		cfg:     cfg,
	}
}

// Write 执行导出管线，将数据流式写入输出端。
func (p *exportPipeline) Write(ctx context.Context, w io.Writer) error {
	// 检查是否导出为 CSV 格式
	if isCSV(p.cfg.sheetName) {
		return p.writeCSV(ctx, w)
	}

	// 检查是否多工作表导出
	if multiSheets, ok := p.export.(WithMultipleSheets); ok {
		return p.writeMultipleSheets(ctx, multiSheets, w)
	}

	// 单工作表导出（无论 FromSlice 还是 FromQuery，底层均使用 excelize.StreamWriter 保持 O(1) 内存）
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheetName := resolveSheetTitle(p.export, p.cfg.sheetName)
	ensureSheet(f, sheetName)

	if err := p.writeSingleSheet(ctx, f, sheetName, p.export); err != nil {
		return err
	}

	return f.Write(w)
}

func (p *exportPipeline) writeMultipleSheets(ctx context.Context, ms WithMultipleSheets, w io.Writer) error {
	sheets := ms.Sheets()
	if len(sheets) == 0 {
		return errors.New("excel: WithMultipleSheets returned an empty sheets slice")
	}

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	firstSheetCreated := false
	for i, sheetExport := range sheets {
		subSheetName := resolveSheetTitle(sheetExport, fmt.Sprintf("Sheet%d", i+1))

		if !firstSheetCreated {
			ensureSheet(f, subSheetName)
			firstSheetCreated = true
		} else {
			_, _ = f.NewSheet(subSheetName)
		}

		if err := p.writeSingleSheet(ctx, f, subSheetName, sheetExport); err != nil {
			return err
		}
	}

	return f.Write(w)
}

func prepareSheetContext(
	f *excelize.File,
	sheetName string,
	exportObj any,
	cfg *exportConfig,
) (int, map[int]int, error) {
	applyFreezePanes(f, sheetName, exportObj, cfg)

	headerStyleID, err := resolveHeaderStyleID(f, exportObj, cfg)
	if err != nil {
		return 0, nil, err
	}

	colStyleIDs := resolveColumnStyleIDs(f, exportObj, cfg)
	return headerStyleID, colStyleIDs, nil
}

func writeInitialHeadings(
	sw *excelize.StreamWriter,
	headings []string,
	headerStyleID int,
	widths map[int]int,
) (int, error) {
	if len(headings) == 0 {
		return 1, nil
	}

	cell, _ := excelize.CoordinatesToCellName(1, 1)
	vals := make([]any, len(headings))
	for idx, header := range headings {
		if headerStyleID > 0 {
			vals[idx] = excelize.Cell{Value: header, StyleID: headerStyleID}
		} else {
			vals[idx] = header
		}
		widths[idx] = len([]rune(header))
	}

	if err := sw.SetRow(cell, vals); err != nil {
		return 1, err
	}
	return 2, nil
}

func (p *exportPipeline) writeSingleSheet(
	ctx context.Context,
	f *excelize.File,
	baseSheetName string,
	exportObj any,
) error {
	headerStyleID, colStyleIDs, err := prepareSheetContext(f, baseSheetName, exportObj, p.cfg)
	if err != nil {
		return err
	}

	sw, err := f.NewStreamWriter(baseSheetName)
	if err != nil {
		return oops.Code(errs.CodeExcelStreamError).Wrap(err)
	}

	headings := resolveHeadings(exportObj, p.cfg)
	widths := make(map[int]int)
	rowOffset, err := writeInitialHeadings(sw, headings, headerStyleID, widths)
	if err != nil {
		return err
	}

	currentSheet, widths, err := p.streamSheetData(
		ctx, f, sw, baseSheetName, exportObj, headings, headerStyleID, colStyleIDs, widths, rowOffset,
	)
	if err != nil {
		return err
	}

	p.applyStreamColumnWidthsFor(f, currentSheet, widths, exportObj)

	if s, ok := exportObj.(WithStyles); ok {
		if err := s.Styles(f, currentSheet); err != nil {
			return fmt.Errorf("failed to apply custom styles on sheet %s: %w", currentSheet, err)
		}
	}

	return nil
}

type streamSheetState struct {
	file             *excelize.File
	sw               *excelize.StreamWriter
	currentSheet     string
	baseSheet        string
	rowOffset        int
	widths           map[int]int
	maxRowsPerSheet  int
	sheetIndex       int
	totalRowsWritten int
	headings         []string
	headerStyleID    int
	colStyleIDs      map[int]int
	exportObj        any
	cfg              *exportConfig
	mapper           func(row any) []any
	pipeline         *exportPipeline
}

func (s *streamSheetState) writeRow(rowItem any, isSQLValues bool) error {
	if s.rowOffset > s.maxRowsPerSheet {
		s.sheetIndex++
		var switchErr error
		s.sw, s.currentSheet, s.rowOffset, s.widths, switchErr = s.pipeline.switchStreamSheet(
			s.file, s.currentSheet, s.baseSheet, s.sw, s.sheetIndex, s.widths, s.headings, s.headerStyleID, s.exportObj,
		)
		if switchErr != nil {
			return switchErr
		}
	}

	var cols []any
	switch {
	case s.mapper != nil:
		cols = s.mapper(rowItem)
	case isSQLValues:
		if sqlCols, ok := rowItem.([]any); ok {
			cols = sqlCols
		}
	default:
		cols = getRowValuesFromStruct(rowItem)
	}

	vals := make([]any, len(cols))
	for colIdx, col := range cols {
		formattedVal := formatValueWithOptions(col, s.cfg.sanitizeFormulas)
		if sID, hasStyle := s.colStyleIDs[colIdx]; hasStyle {
			vals[colIdx] = excelize.Cell{Value: formattedVal, StyleID: sID}
		} else {
			vals[colIdx] = formattedVal
		}

		if s.totalRowsWritten < widthSampleLimit {
			cellLen := len([]rune(fmt.Sprintf("%v", formattedVal)))
			if current, exists := s.widths[colIdx]; !exists || cellLen > current {
				s.widths[colIdx] = cellLen
			}
		}
	}

	cell, _ := excelize.CoordinatesToCellName(1, s.rowOffset)
	if err := s.sw.SetRow(cell, vals); err != nil {
		return err
	}

	s.rowOffset++
	s.totalRowsWritten++
	if s.cfg.onProgress != nil {
		s.cfg.onProgress(s.totalRowsWritten)
	}
	if ev, ok := s.exportObj.(WithEvents); ok {
		ev.OnProgress(s.totalRowsWritten, 0)
	}
	return nil
}

func (p *exportPipeline) streamSheetData(
	ctx context.Context,
	f *excelize.File,
	sw *excelize.StreamWriter,
	baseSheetName string,
	exportObj any,
	headings []string,
	headerStyleID int,
	colStyleIDs map[int]int,
	widths map[int]int,
	rowOffset int,
) (string, map[int]int, error) {
	state := &streamSheetState{
		file:            f,
		sw:              sw,
		currentSheet:    baseSheetName,
		baseSheet:       baseSheetName,
		rowOffset:       rowOffset,
		widths:          widths,
		maxRowsPerSheet: p.getSheetChunkSizeFor(exportObj),
		sheetIndex:      1,
		headings:        headings,
		headerStyleID:   headerStyleID,
		colStyleIDs:     colStyleIDs,
		exportObj:       exportObj,
		cfg:             p.cfg,
		mapper:          resolveMapper(exportObj, p.cfg),
		pipeline:        p,
	}

	switch exp := exportObj.(type) {
	case FromChunkedQuery:
		if err := p.writeFromChunkedQuery(ctx, exp, state); err != nil {
			return "", nil, err
		}
	case FromQuery:
		if err := p.writeFromQuery(ctx, exp, state); err != nil {
			return "", nil, err
		}
	case FromSlice:
		if err := p.writeFromSlice(ctx, exp, state); err != nil {
			return "", nil, err
		}
	default:
		return "", nil, errors.New("export object must implement FromSlice, FromQuery, or FromChunkedQuery")
	}

	if err := state.sw.Flush(); err != nil {
		return "", nil, oops.Code(errs.CodeExcelFlushError).Wrap(err)
	}

	return state.currentSheet, state.widths, nil
}

func (p *exportPipeline) writeFromChunkedQuery(ctx context.Context, chunked FromChunkedQuery, state *streamSheetState) error {
	chunkSize := chunked.ChunkSize()
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}

	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		chunk, err := chunked.FetchChunk(ctx, offset, chunkSize)
		if err != nil {
			return fmt.Errorf("failed to fetch query chunk at offset %d: %w", offset, err)
		}

		if len(chunk) == 0 {
			break
		}

		for _, item := range chunk {
			if p.cfg.maxRows > 0 && state.totalRowsWritten >= p.cfg.maxRows {
				return nil
			}

			if err := state.writeRow(item, false); err != nil {
				return err
			}
		}

		offset += len(chunk)
		if len(chunk) < chunkSize {
			break
		}
	}

	return nil
}

func (p *exportPipeline) writeFromQuery(ctx context.Context, fromQuery FromQuery, state *streamSheetState) error {
	rows, err := p.fetchQueryRowsFor(ctx, fromQuery)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}

		if p.cfg.maxRows > 0 && state.totalRowsWritten >= p.cfg.maxRows {
			break
		}

		values, err := scanSQLRowValues(rows)
		if err != nil {
			return err
		}

		if err := state.writeRow(values, true); err != nil {
			return err
		}
	}

	return rows.Err()
}

func (p *exportPipeline) writeFromSlice(ctx context.Context, fromSlice FromSlice, state *streamSheetState) error {
	val := reflect.ValueOf(fromSlice.FromSlice())
	if val.Kind() == reflect.Pointer {
		val = val.Elem()
	}
	if val.Kind() != reflect.Slice {
		return errors.New("excel: FromSlice must return a slice or slice pointer")
	}

	length := val.Len()
	for i := range length {
		if err := ctx.Err(); err != nil {
			return err
		}

		if p.cfg.maxRows > 0 && state.totalRowsWritten >= p.cfg.maxRows {
			break
		}

		item := val.Index(i).Interface()
		if err := state.writeRow(item, false); err != nil {
			return err
		}
	}
	return nil
}

func (p *exportPipeline) switchStreamSheet(
	file *excelize.File,
	currentSheet string,
	baseSheet string,
	oldSW *excelize.StreamWriter,
	sheetIndex int,
	widths map[int]int,
	headings []string,
	headerStyleID int,
	exportObj any,
) (*excelize.StreamWriter, string, int, map[int]int, error) {
	if err := oldSW.Flush(); err != nil {
		return nil, "", 0, nil, err
	}
	p.applyStreamColumnWidthsFor(file, currentSheet, widths, exportObj)

	newSheet := fmt.Sprintf("%s_%d", baseSheet, sheetIndex)
	_, _ = file.NewSheet(newSheet)

	applyFreezePanes(file, newSheet, exportObj, p.cfg)

	sw, err := file.NewStreamWriter(newSheet)
	if err != nil {
		return nil, "", 0, nil, err
	}

	newWidths := make(map[int]int)
	rowOffset := 1

	if len(headings) > 0 {
		cell, _ := excelize.CoordinatesToCellName(1, rowOffset)
		vals := make([]any, len(headings))
		for idx, header := range headings {
			if headerStyleID > 0 {
				vals[idx] = excelize.Cell{Value: header, StyleID: headerStyleID}
			} else {
				vals[idx] = header
			}
			newWidths[idx] = len([]rune(header))
		}
		if err := sw.SetRow(cell, vals); err != nil {
			return nil, "", 0, nil, err
		}
		rowOffset++
	}

	return sw, newSheet, rowOffset, newWidths, nil
}

func (p *exportPipeline) writeCSV(ctx context.Context, w io.Writer) error {
	if _, isMulti := p.export.(WithMultipleSheets); isMulti {
		return errors.New("CSV format does not support multiple sheets export")
	}

	cw := csv.NewWriter(w)

	headings := resolveHeadings(p.export, p.cfg)
	if len(headings) > 0 {
		if err := cw.Write(headings); err != nil {
			return err
		}
	}

	mapper := resolveMapper(p.export, p.cfg)

	switch exp := p.export.(type) {
	case FromChunkedQuery:
		return p.writeCSVFromChunkedQuery(ctx, cw, exp, mapper)
	case FromQuery:
		return p.writeCSVFromQuery(ctx, cw, exp, mapper)
	case FromSlice:
		return p.writeCSVFromSlice(ctx, cw, exp, mapper)
	default:
		return errors.New("export object must implement FromSlice, FromQuery, or FromChunkedQuery")
	}
}

func (p *exportPipeline) writeCSVFromChunkedQuery(
	ctx context.Context,
	cw *csv.Writer,
	chunked FromChunkedQuery,
	mapper func(any) []any,
) error {
	chunkSize := chunked.ChunkSize()
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}

	offset := 0
	totalWritten := 0

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		chunk, err := chunked.FetchChunk(ctx, offset, chunkSize)
		if err != nil {
			return fmt.Errorf("failed to fetch query chunk at offset %d: %w", offset, err)
		}

		if len(chunk) == 0 {
			break
		}

		for _, item := range chunk {
			if p.cfg.maxRows > 0 && totalWritten >= p.cfg.maxRows {
				cw.Flush()
				return cw.Error()
			}

			var cols []any
			if mapper != nil {
				cols = mapper(item)
			} else {
				cols = getRowValuesFromStruct(item)
			}

			if err := cw.Write(p.formatCSVRecord(cols)); err != nil {
				return err
			}
			totalWritten++
			if p.cfg.onProgress != nil {
				p.cfg.onProgress(totalWritten)
			}
			if ev, ok := p.export.(WithEvents); ok {
				ev.OnProgress(totalWritten, 0)
			}
		}

		offset += len(chunk)
		if len(chunk) < chunkSize {
			break
		}
	}

	cw.Flush()
	return cw.Error()
}

func (p *exportPipeline) writeCSVFromQuery(
	ctx context.Context,
	cw *csv.Writer,
	fromQuery FromQuery,
	mapper func(any) []any,
) error {
	rows, err := p.fetchQueryRowsFor(ctx, fromQuery)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	totalWritten := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}

		if p.cfg.maxRows > 0 && totalWritten >= p.cfg.maxRows {
			break
		}

		values, err := scanSQLRowValues(rows)
		if err != nil {
			return err
		}

		cols := values
		if mapper != nil {
			cols = mapper(values)
		}

		if err := cw.Write(p.formatCSVRecord(cols)); err != nil {
			return err
		}
		totalWritten++
		if p.cfg.onProgress != nil {
			p.cfg.onProgress(totalWritten)
		}
		if ev, ok := p.export.(WithEvents); ok {
			ev.OnProgress(totalWritten, 0)
		}
	}

	cw.Flush()
	return errors.Join(rows.Err(), cw.Error())
}

func (p *exportPipeline) writeCSVFromSlice(
	ctx context.Context,
	cw *csv.Writer,
	fromSlice FromSlice,
	mapper func(any) []any,
) error {
	val := reflect.ValueOf(fromSlice.FromSlice())
	if val.Kind() == reflect.Pointer {
		val = val.Elem()
	}
	if val.Kind() != reflect.Slice {
		return errors.New("excel: FromSlice must return a slice or slice pointer")
	}

	length := val.Len()
	for i := range length {
		if err := ctx.Err(); err != nil {
			return err
		}

		if p.cfg.maxRows > 0 && i >= p.cfg.maxRows {
			break
		}

		item := val.Index(i).Interface()
		var cols []any
		if mapper != nil {
			cols = mapper(item)
		} else {
			cols = getRowValuesFromStruct(item)
		}

		if err := cw.Write(p.formatCSVRecord(cols)); err != nil {
			return err
		}
		if p.cfg.onProgress != nil {
			p.cfg.onProgress(i + 1)
		}
		if ev, ok := p.export.(WithEvents); ok {
			ev.OnProgress(i+1, 0)
		}
	}

	cw.Flush()
	return cw.Error()
}

func (p *exportPipeline) formatCSVRecord(cols []any) []string {
	return lo.Map(cols, func(col any, _ int) string {
		return fmt.Sprintf("%v", formatValueWithOptions(col, p.cfg.sanitizeFormulas))
	})
}

var errDBUnavailable = errors.New("database connection is not available in excel service")

func resolveSQLDB(cfg *exportConfig, svc *Service) (*sql.DB, error) {
	if cfg != nil && cfg.db != nil {
		return cfg.db, nil
	}
	if svc != nil && svc.db != nil {
		return svc.db, nil
	}
	return nil, errDBUnavailable
}

func (p *exportPipeline) fetchQueryRowsFor(ctx context.Context, fromQuery FromQuery) (*sql.Rows, error) {
	sqlDB, err := resolveSQLDB(p.cfg, p.service)
	if err != nil {
		return nil, err
	}
	return fromQuery.QueryRows(ctx, sqlDB)
}

func (p *exportPipeline) getSheetChunkSizeFor(exportObj any) int {
	if p.cfg != nil && p.cfg.sheetChunkSize > 0 {
		return p.cfg.sheetChunkSize
	}
	if c, ok := exportObj.(WithSheetChunkSize); ok && c.SheetChunkSize() > 0 {
		return c.SheetChunkSize()
	}
	return defaultMaxRowsPerSheet
}

func scanSQLRowValues(rows *sql.Rows) ([]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	values := make([]any, len(cols))
	scanArgs := make([]any, len(cols))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	if err := rows.Scan(scanArgs...); err != nil {
		return nil, err
	}

	for i, v := range values {
		if b, ok := v.([]byte); ok {
			values[i] = string(b)
		}
	}

	return values, nil
}

func applyFreezePanes(f *excelize.File, sheetName string, exportObj any, cfg *exportConfig) {
	freezeCol := 0
	freezeRow := 0
	if fp, ok := exportObj.(WithFreezePanes); ok {
		freezeCol, freezeRow = fp.FreezePanes()
	} else if cfg != nil && cfg.freezePanes {
		freezeRow = 1
	}

	if freezeRow > 0 || freezeCol > 0 {
		topLeftCell, _ := excelize.CoordinatesToCellName(freezeCol+1, freezeRow+1)
		activePane := "bottomLeft"
		if freezeCol > 0 && freezeRow > 0 {
			activePane = "bottomRight"
		} else if freezeCol > 0 {
			activePane = "topRight"
		}

		_ = f.SetPanes(sheetName, &excelize.Panes{
			Freeze:      true,
			Split:       false,
			XSplit:      freezeCol,
			YSplit:      freezeRow,
			TopLeftCell: topLeftCell,
			ActivePane:  activePane,
		})
	}
}

func resolveHeaderStyleID(f *excelize.File, exportObj any, cfg *exportConfig) (int, error) {
	var headerStyle *excelize.Style
	if hs, ok := exportObj.(WithHeaderStyle); ok {
		headerStyle = hs.HeaderStyle()
	} else if cfg != nil && cfg.headerStyle != nil {
		headerStyle = cfg.headerStyle
	}

	if headerStyle != nil {
		sID, err := f.NewStyle(headerStyle)
		if err != nil {
			return 0, oops.Code(errs.CodeExcelStyleError).Wrap(err)
		}
		return sID, nil
	}
	return 0, nil
}

func resolveColumnStyleIDs(f *excelize.File, exportObj any, cfg *exportConfig) map[int]int {
	var colFormats map[string]string
	if cf, ok := exportObj.(WithColumnFormatting); ok {
		colFormats = cf.ColumnFormats()
	} else if cfg != nil && cfg.columnFormats != nil {
		colFormats = cfg.columnFormats
	}

	colStyleIDs := make(map[int]int)
	for colName, fmtStr := range colFormats {
		colNum, err := excelize.ColumnNameToNumber(colName)
		if err == nil {
			customFmt := fmtStr
			sID, err := f.NewStyle(&excelize.Style{CustomNumFmt: &customFmt})
			if err == nil {
				colStyleIDs[colNum-1] = sID
			}
		}
	}
	return colStyleIDs
}

func resolveHeadings(exportObj any, cfg *exportConfig) []string {
	if cfg != nil && len(cfg.headings) > 0 {
		return cfg.headings
	}
	if h, ok := exportObj.(WithHeadings); ok {
		return h.Headings()
	}
	return nil
}

func resolveMapper(exportObj any, cfg *exportConfig) func(row any) []any {
	if m, ok := exportObj.(WithMapping); ok {
		return m.Mapping
	}
	if cfg != nil && cfg.mapping != nil {
		return cfg.mapping
	}
	return nil
}

func (p *exportPipeline) applyStreamColumnWidthsFor(
	f *excelize.File,
	sheetName string,
	widths map[int]int,
	exportObj any,
) {
	if p.cfg != nil && p.cfg.columnWidths != nil {
		for colStr, width := range p.cfg.columnWidths {
			_ = f.SetColWidth(sheetName, colStr, colStr, width)
		}
		return
	}

	if customWidths, ok := exportObj.(WithColumnWidths); ok {
		for colStr, width := range customWidths.ColumnWidths() {
			_ = f.SetColWidth(sheetName, colStr, colStr, width)
		}
		return
	}

	shouldAuto := false
	if p.cfg != nil && p.cfg.autoWidth != nil {
		shouldAuto = *p.cfg.autoWidth
	} else if autoSize, ok := exportObj.(ShouldAutoSize); ok {
		shouldAuto = autoSize.ShouldAutoSize()
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

func ensureSheet(f *excelize.File, sheetName string) {
	if sheetName == defaultSheetName {
		return
	}

	_, _ = f.NewSheet(sheetName)
	_ = f.DeleteSheet(defaultSheetName)
}

func isCSV(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".csv")
}

func resolveSheetTitle(export any, fallback string) string {
	if t, ok := export.(WithTitle); ok {
		return cmp.Or(t.Title(), fallback)
	}
	return fallback
}
