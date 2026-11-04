package excel

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dromara/carbon/v2"
	"github.com/xuri/excelize/v2"

	"cgin/pkg/errs"
)

const (
	// maxColumnWidth 是自动列宽上限，避免超长文本把表格撑爆。
	maxColumnWidth = 60
	// defaultColumnWidth 是未指定列宽时的默认列宽。
	defaultColumnWidth = 15.0
)

type writerConfig struct {
	noSanitize bool
}

// WriterOption 定义流式写出配置选项。
type WriterOption func(*writerConfig)

// WithNoSanitize 关闭公式注入转义（仅限内部高信报表导出）。
func WithNoSanitize() WriterOption {
	return func(c *writerConfig) {
		c.noSanitize = true
	}
}

// Writer 管理 xlsx 工作簿的流式写出生命周期，支持单工作表与多工作表输出。
type Writer struct {
	w           io.Writer
	file        *excelize.File
	sheets      map[string]bool
	closers     []func() error
	firstSheet  string
	alignStyles map[AlignMode]int
	closed      bool
	noSanitize  bool
}

// NewWriter 构造一个面向 w 的流式 Excel 写出器。
func NewWriter(w io.Writer, opts ...WriterOption) *Writer {
	cfg := writerConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	file := excelize.NewFile()
	alignStyles := make(map[AlignMode]int, 3)
	for _, mode := range []AlignMode{AlignLeft, AlignCenter, AlignRight} {
		if styleID, err := file.NewStyle(&excelize.Style{
			Alignment: &excelize.Alignment{
				Horizontal: string(mode),
				Vertical:   "center",
			},
		}); err == nil {
			alignStyles[mode] = styleID
		}
	}

	return &Writer{
		w:           w,
		file:        file,
		sheets:      make(map[string]bool),
		alignStyles: alignStyles,
		noSanitize:  cfg.noSanitize,
	}
}

// NewSheet 在工作簿中开辟一个新工作表，并返回泛型强类型流式 SheetWriter。
//
// 若为工作簿创建的首个工作表且表名不为 DefaultSheetName，将就地重命名底层默认表，避免遗留空白 Sheet；
// 多 Sheet 导出场景下，各 SheetWriter 建议在完成写入后及时调用 Close 刷新流式缓冲区。
func NewSheet[T any](w *Writer, sheet string, cols []ExportColumn[T]) (*SheetWriter[T], error) {
	if w.closed {
		return nil, errors.New("excel: writer is already closed")
	}

	sheetName := strings.TrimSpace(sheet)
	if sheetName == "" {
		sheetName = DefaultSheetName
	}

	if w.sheets[sheetName] {
		return nil, fmt.Errorf("excel: sheet %q already exists", sheetName)
	}

	if len(w.sheets) == 0 {
		w.firstSheet = sheetName
		if err := renameDefaultSheet(w.file, sheetName); err != nil {
			return nil, err
		}
	} else {
		_, err := w.file.NewSheet(sheetName)
		if err != nil {
			return nil, errs.Wrap(errs.CodeExcelStreamError, fmt.Sprintf("failed to create sheet %q", sheetName), err)
		}
	}
	w.sheets[sheetName] = true

	if err := freezeHeaderRow(w.file, sheetName); err != nil {
		return nil, err
	}

	streamWriter, err := w.file.NewStreamWriter(sheetName)
	if err != nil {
		return nil, errs.Wrap(errs.CodeExcelStreamError, fmt.Sprintf("failed to create stream writer for sheet %q", sheetName), err)
	}

	sw := &SheetWriter[T]{
		writer:      streamWriter,
		cols:        cols,
		sheetName:   sheetName,
		rowIndex:    1,
		alignStyles: w.alignStyles,
		noSanitize:  w.noSanitize,
	}

	if len(cols) > 0 {
		for colIdx, col := range cols {
			width := col.Width
			if width <= 0 {
				width = defaultColumnWidth
			} else if width > maxColumnWidth {
				width = maxColumnWidth
			}
			colNum := colIdx + 1
			if colErr := streamWriter.SetColWidth(colNum, colNum, width); colErr != nil {
				return nil, errs.Wrap(errs.CodeExcelStreamError, "failed to set excel column width", colErr)
			}
		}

		headers := make([]string, len(cols))
		for i, c := range cols {
			headers[i] = c.Header
		}
		if err := writeHeaderRow(w.file, streamWriter, headers); err != nil {
			return nil, err
		}
		sw.rowIndex++
	}

	w.closers = append(w.closers, sw.Close)
	return sw, nil
}

// Close 刷新所有未关闭的工作表流并关闭工作簿，将完整 xlsx 二进制数据写回底层 io.Writer。
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	defer func() { _ = w.file.Close() }()

	for _, closeSheet := range w.closers {
		if err := closeSheet(); err != nil {
			return err
		}
	}

	if w.w == nil {
		return errors.New("excel: nil output writer")
	}
	if err := w.file.Write(w.w); err != nil {
		return errs.Wrap(errs.CodeExcelStreamWriteError, "failed to write excel workbook to output", err)
	}
	return nil
}

// SheetWriter 实现单工作表的 O(1) 内存流式行写入。
type SheetWriter[T any] struct {
	writer      *excelize.StreamWriter
	cols        []ExportColumn[T]
	sheetName   string
	rowIndex    int
	alignStyles map[AlignMode]int
	noSanitize  bool
	closed      bool
}

// WriteRow 写入单行领域实体。
func (s *SheetWriter[T]) WriteRow(item *T) error {
	if s.closed {
		return errors.New("excel: sheet writer is closed")
	}
	if s.rowIndex > MaxRowsPerSheet {
		return errs.New(errs.CodeExcelRowsError, fmt.Sprintf("excel: sheet %q exceeds %d rows", s.sheetName, MaxRowsPerSheet))
	}

	cells := make([]any, len(s.cols))
	for i, col := range s.cols {
		var raw any
		if col.Encode != nil {
			raw = col.Encode(item)
		}
		val := formatValue(raw, s.noSanitize)
		styleID := 0
		if col.Align != "" {
			styleID = s.alignStyles[col.Align]
		}
		cells[i] = excelize.Cell{Value: val, StyleID: styleID}
	}

	cellName, err := excelize.CoordinatesToCellName(1, s.rowIndex)
	if err != nil {
		return errs.Wrap(errs.CodeExcelStreamError, "failed to resolve cell coordinates", err)
	}
	if err := s.writer.SetRow(cellName, cells); err != nil {
		return errs.Wrap(errs.CodeExcelStreamError, "failed to write excel row", err)
	}

	s.rowIndex++
	return nil
}

// WriteBatch 批量写入多行领域实体。
func (s *SheetWriter[T]) WriteBatch(items []T) error {
	for i := range items {
		if err := s.WriteRow(&items[i]); err != nil {
			return err
		}
	}
	return nil
}

// Close 刷新当前 Sheet 的流式缓冲区。
func (s *SheetWriter[T]) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.writer.Flush(); err != nil {
		return errs.Wrap(errs.CodeExcelFlushError, "failed to flush excel stream writer", err)
	}
	return nil
}

func renameDefaultSheet(file *excelize.File, sheetName string) error {
	if sheetName == DefaultSheetName {
		return nil
	}
	if err := file.SetSheetName(DefaultSheetName, sheetName); err != nil {
		return errs.Wrap(errs.CodeExcelStreamError, "failed to rename default excel sheet", err)
	}
	return nil
}

func freezeHeaderRow(file *excelize.File, sheetName string) error {
	panes := &excelize.Panes{
		Freeze:      true,
		Split:       false,
		XSplit:      0,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft",
	}
	if err := file.SetPanes(sheetName, panes); err != nil {
		return errs.Wrap(errs.CodeExcelStreamError, "failed to freeze excel header row", err)
	}
	return nil
}

func writeHeaderRow(file *excelize.File, writer *excelize.StreamWriter, header []string) error {
	styleID, err := file.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#F3F4F6"}},
		Border: []excelize.Border{
			{Type: "bottom", Color: "#D1D5DB", Style: 1},
		},
		Alignment: &excelize.Alignment{
			Vertical: "center",
		},
	})
	if err != nil {
		return errs.Wrap(errs.CodeExcelStyleError, "failed to create excel header style", err)
	}

	cells := make([]any, len(header))
	for i, title := range header {
		cells[i] = excelize.Cell{Value: title, StyleID: styleID}
	}

	cellName, err := excelize.CoordinatesToCellName(1, 1)
	if err != nil {
		return errs.Wrap(errs.CodeExcelStreamError, "failed to resolve excel header cell coordinates", err)
	}
	if err := writer.SetRow(cellName, cells); err != nil {
		return errs.Wrap(errs.CodeExcelStreamError, "failed to write excel header row", err)
	}
	return nil
}

// formatValue 把任意 Go 值规格化为安全的单元格值。
func formatValue(value any, noSanitize bool) any {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		if noSanitize {
			return typed
		}
		return sanitizeFormula(typed)
	case time.Time:
		if typed.IsZero() {
			return ""
		}
		return carbon.CreateFromStdTime(typed).ToDateTimeString()
	case *time.Time:
		if typed == nil {
			return ""
		}
		return formatValue(*typed, noSanitize)
	case bool:
		if typed {
			return "Yes"
		}
		return "No"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return typed
	default:
		s := fmt.Sprintf("%v", typed)
		if noSanitize {
			return s
		}
		return sanitizeFormula(s)
	}
}

// sanitizeFormula 为可能触发公式执行的前缀补单引号，使其按纯文本落库（防范 CWE-1236 公式注入）。
func sanitizeFormula(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	default:
		return value
	}
}
