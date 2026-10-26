// Package excel 提供低内存占用的 xlsx 流式写出能力。
//
// 对外只有一条写入路径：表头 + 行迭代器 → io.Writer。样式、公式注入防护、
// 单表行数上限等行为全部收敛在实现内部，调用方无法逐个关闭。
package excel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dromara/carbon/v2"
	"github.com/samber/oops"
	"github.com/xuri/excelize/v2"

	"cgin/pkg/errs"
)

const (
	// MaxRowsPerSheet 是 xlsx 单工作表行数硬上限（含表头），由 Excel 规范决定。
	MaxRowsPerSheet = 1_048_576
	// defaultSheetName 是未指定工作表名时使用的名称。
	defaultSheetName = "Sheet1"
	// maxColumnWidth 是自动列宽上限，避免超长文本把表格撑爆。
	maxColumnWidth = 60
)

// RowIterator 按行产出待写入的单元格值；ok 为 false 表示迭代结束。
type RowIterator func() (row []any, ok bool, err error)

// Column 描述强类型表格单列的双向映射契约（零反射声明式 Schema）。
type Column[T any] struct {
	// Header 工作表首行表头列名。
	Header string
	// Required 导入时该列是否必填（表头必须存在且单元格非空）。
	Required bool
	// Encode 导出时将领域实体投影为单元格值的纯函数。
	Encode func(row *T) any
	// Decode 导入时将单元格文本解析回领域实体字段的纯函数。
	Decode func(cell string, dst *T) error
}

// WriteTable 基于声明式 Column[T] 列契约，将类型安全迭代器产出的实体流式写入 xlsx。
func WriteTable[T any](
	ctx context.Context,
	w io.Writer,
	sheet string,
	cols []Column[T],
	next func() (T, bool, error),
) error {
	if next == nil {
		return errors.New("excel: nil table iterator")
	}

	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = c.Header
	}

	return Write(ctx, w, sheet, header, func() ([]any, bool, error) {
		item, ok, err := next()
		if err != nil || !ok {
			return nil, ok, err
		}
		row := make([]any, len(cols))
		for i, c := range cols {
			if c.Encode != nil {
				row[i] = c.Encode(&item)
			}
		}
		return row, true, nil
	})
}

// Write 把表头与迭代器产出的行流式写入单个工作表的 xlsx 并落到 w。
//
// 内存占用为 O(1)：行数据逐条写入流式写入器，不缓存全量数据。
// 行数超过 MaxRowsPerSheet 时返回错误而不是静默截断；ctx 取消时立即中止。
func Write(ctx context.Context, w io.Writer, sheet string, header []string, next RowIterator) error {
	if w == nil {
		return errors.New("excel: nil writer")
	}
	if next == nil {
		return errors.New("excel: nil row iterator")
	}

	sheetName := strings.TrimSpace(sheet)
	if sheetName == "" {
		sheetName = defaultSheetName
	}

	file := excelize.NewFile()
	defer func() { _ = file.Close() }()

	if err := renameDefaultSheet(file, sheetName); err != nil {
		return err
	}
	if err := freezeHeaderRow(file, sheetName); err != nil {
		return err
	}

	writer, err := file.NewStreamWriter(sheetName)
	if err != nil {
		return oops.Code(errs.CodeExcelStreamError).Wrap(err)
	}

	rowIndex := 1
	if len(header) > 0 {
		// 流式写入器要求列宽先于任何行写入确定，否则 SetColWidth 会报错。
		if err := writer.SetColWidth(1, len(header), maxColumnWidth); err != nil {
			return oops.Code(errs.CodeExcelStreamError).Wrap(err)
		}
		if err := writeHeaderRow(file, writer, header); err != nil {
			return err
		}
		rowIndex++
	}

	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		row, ok, iterErr := next()
		if iterErr != nil {
			return oops.Code(errs.CodeExcelGeneratorError).Wrap(iterErr)
		}
		if !ok {
			break
		}
		if rowIndex > MaxRowsPerSheet {
			return oops.Code(errs.CodeExcelRowsError).
				Errorf("excel: sheet %q exceeds %d rows", sheetName, MaxRowsPerSheet)
		}
		if err := writeRow(writer, row, rowIndex); err != nil {
			return err
		}
		rowIndex++
	}

	if err := writer.Flush(); err != nil {
		return oops.Code(errs.CodeExcelFlushError).Wrap(err)
	}
	if err := file.Write(w); err != nil {
		return oops.Code(errs.CodeExcelStreamWriteError).Wrap(err)
	}
	return nil
}

// renameDefaultSheet 把 excelize 预置的 Sheet1 改名为目标表名，避免残留空工作表。
func renameDefaultSheet(file *excelize.File, sheetName string) error {
	if sheetName == defaultSheetName {
		return nil
	}
	if err := file.SetSheetName(defaultSheetName, sheetName); err != nil {
		return oops.Code(errs.CodeExcelStreamError).Wrap(err)
	}
	return nil
}

// freezeHeaderRow 冻结首行，保证长表滚动时表头始终可见。
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
		return oops.Code(errs.CodeExcelStreamError).Wrap(err)
	}
	return nil
}

// writeHeaderRow 写入带底纹与加粗样式的表头行。
func writeHeaderRow(file *excelize.File, writer *excelize.StreamWriter, header []string) error {
	styleID, err := file.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#F3F4F6"}},
		Border: []excelize.Border{
			{Type: "bottom", Color: "#D1D5DB", Style: 1},
		},
	})
	if err != nil {
		return oops.Code(errs.CodeExcelStyleError).Wrap(err)
	}

	cells := make([]any, len(header))
	for i, title := range header {
		cells[i] = excelize.Cell{Value: title, StyleID: styleID}
	}

	cellName, err := excelize.CoordinatesToCellName(1, 1)
	if err != nil {
		return oops.Code(errs.CodeExcelStreamError).Wrap(err)
	}
	if err := writer.SetRow(cellName, cells); err != nil {
		return oops.Code(errs.CodeExcelStreamError).Wrap(err)
	}
	return nil
}

// writeRow 写入一行数据，并对每个单元格做类型规格化与公式注入转义。
func writeRow(writer *excelize.StreamWriter, row []any, rowIndex int) error {
	cells := make([]any, len(row))
	for i, value := range row {
		cells[i] = excelize.Cell{Value: formatValue(value)}
	}

	cellName, err := excelize.CoordinatesToCellName(1, rowIndex)
	if err != nil {
		return oops.Code(errs.CodeExcelStreamError).Wrap(err)
	}
	if err := writer.SetRow(cellName, cells); err != nil {
		return oops.Code(errs.CodeExcelStreamError).Wrap(err)
	}
	return nil
}

// formatValue 把任意 Go 值规格化为安全的单元格值。
//
// 文本一律经过公式注入转义：以 = + - @ \t \r 开头的单元格等价于可执行公式（CWE-1236）。
func formatValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return sanitizeFormula(typed)
	case time.Time:
		if typed.IsZero() {
			return ""
		}
		return carbon.CreateFromStdTime(typed).ToDateTimeString()
	case *time.Time:
		if typed == nil || typed.IsZero() {
			return ""
		}
		return carbon.CreateFromStdTime(*typed).ToDateTimeString()
	case bool:
		if typed {
			return "Yes"
		}
		return "No"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return typed
	default:
		return sanitizeFormula(fmt.Sprintf("%v", typed))
	}
}

// sanitizeFormula 为可能触发公式执行的前缀补单引号，使其按纯文本落库。
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
