// Package excel 提供低内存占用的 xlsx 流式写出与读取能力。
//
// 导出基于 ExportColumn[T] 实现纯函数式零反射投影与多 Sheet 管理；
// 导入基于 ImportColumn[T] 实现游标流式解析、必填/默认值处理、文件内唯一键去重与结构化行级错误汇报。
package excel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/xuri/excelize/v2"

	"cgin/pkg/errs"
)

const (
	// MaxRowsPerSheet 是 xlsx 单工作表行数硬上限（含表头），由 Excel 规范决定。
	MaxRowsPerSheet = 1_048_576
	// DefaultSheetName 是未指定工作表名时使用的默认名称。
	DefaultSheetName = "Sheet1"
	// DefaultChunkSize 是流式导入的默认分块回调大小。
	DefaultChunkSize = 500
	// DefaultMaxImportBytes 是导入文件的默认最大字节数上限（20 MiB）。
	DefaultMaxImportBytes int64 = 20 << 20
	// MaxValidationErrors 是单次导入最多收集的行级校验错误条数，防止恶意大文件撑爆错误响应体。
	MaxValidationErrors = 50
)

// RowIterator 按行产出待写入的单元格切片值；ok 为 false 表示迭代结束。
type RowIterator func() (row []any, ok bool, err error)

// RowError 描述单行单列的结构化校验错误详情。
type RowError struct {
	Row     int    `json:"row"`     // Excel 真实物理行号（从 1 开始，含表头）
	Field   string `json:"field"`   // 字段唯一标识（如 email, role）
	Header  string `json:"header"`  // Excel 表头展示名（如 "电子邮箱"）
	Message string `json:"message"` // 校验失败原因
}

// ImportResult 封装流式导入的完整统计指标与行级错误报告。
type ImportResult struct {
	TotalRows   int        `json:"totalRows"`   // 读取的数据行总数（不含表头与空白行）
	SuccessRows int        `json:"successRows"` // 成功入库/解析的数据行数
	FailedRows  int        `json:"failedRows"`  // 校验失败或处理失败的行数
	Errors      []RowError `json:"errors"`      // 结构化行级错误明细（最多收集 MaxValidationErrors 条）
}

// HasErrors 报告导入过程中是否存在校验失败行。
func (r ImportResult) HasErrors() bool {
	return len(r.Errors) > 0 || r.FailedRows > 0
}

// WriteTable 基于声明式 ExportColumn[T] 列契约，将类型安全迭代器产出的实体流式写入 xlsx。
//
// 内存占用为 O(1)：数据逐条写入流式写入器，不缓存全量数据。
func WriteTable[T any](
	ctx context.Context,
	w io.Writer,
	sheet string,
	cols []ExportColumn[T],
	next func() (T, bool, error),
	opts ...WriterOption,
) error {
	if next == nil {
		return errors.New("excel: nil table iterator")
	}

	writer := NewWriter(w, opts...)
	defer func() { _ = writer.Close() }()

	sw, err := NewSheet(writer, sheet, cols)
	if err != nil {
		return err
	}

	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		item, ok, iterErr := next()
		if iterErr != nil {
			return errs.Wrap(errs.CodeExcelGeneratorError, "failed to read next row from iterator", iterErr)
		}
		if !ok {
			break
		}

		if err := sw.WriteRow(&item); err != nil {
			return err
		}
	}

	if err := sw.Close(); err != nil {
		return err
	}
	return writer.Close()
}

// Write 把表头与迭代器产出的原生行流式写入单个工作表的 xlsx 并落到 w。
func Write(ctx context.Context, w io.Writer, sheet string, header []string, next RowIterator) error {
	if w == nil {
		return errors.New("excel: nil writer")
	}
	if next == nil {
		return errors.New("excel: nil row iterator")
	}

	sheetName := strings.TrimSpace(sheet)
	if sheetName == "" {
		sheetName = DefaultSheetName
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
		return errs.Wrap(errs.CodeExcelStreamError, "failed to create excel stream writer", err)
	}

	rowIndex := 1
	if len(header) > 0 {
		if err := writer.SetColWidth(1, len(header), maxColumnWidth); err != nil {
			return errs.Wrap(errs.CodeExcelStreamError, "failed to set excel column width", err)
		}
		if err := writeHeaderRow(file, writer, header); err != nil {
			return err
		}
		rowIndex++
	}

	if err := streamRawRows(ctx, writer, sheetName, rowIndex, next); err != nil {
		return err
	}

	if err := writer.Flush(); err != nil {
		return errs.Wrap(errs.CodeExcelFlushError, "failed to flush excel stream writer", err)
	}
	if err := file.Write(w); err != nil {
		return errs.Wrap(errs.CodeExcelStreamWriteError, "failed to write excel workbook to output", err)
	}
	return nil
}

func streamRawRows(
	ctx context.Context,
	writer *excelize.StreamWriter,
	sheetName string,
	startRow int,
	next RowIterator,
) error {
	rowIndex := startRow
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		row, ok, iterErr := next()
		if iterErr != nil {
			return errs.Wrap(errs.CodeExcelGeneratorError, "failed to read next row from iterator", iterErr)
		}
		if !ok {
			return nil
		}
		if rowIndex > MaxRowsPerSheet {
			return errs.New(errs.CodeExcelRowsError, fmt.Sprintf("excel: sheet %q exceeds %d rows", sheetName, MaxRowsPerSheet))
		}

		cells := make([]any, len(row))
		for i, value := range row {
			cells[i] = excelize.Cell{Value: formatValue(value, false)}
		}
		cellName, cErr := excelize.CoordinatesToCellName(1, rowIndex)
		if cErr != nil {
			return errs.Wrap(errs.CodeExcelStreamError, "failed to resolve cell coordinates", cErr)
		}
		if err := writer.SetRow(cellName, cells); err != nil {
			return errs.Wrap(errs.CodeExcelStreamError, "failed to write excel data row", err)
		}
		rowIndex++
	}
}

// ReadTable 基于声明式 ImportColumn[T] 列契约流式读取并解析 xlsx，按分块大小回调 onBatch。
func ReadTable[T any](
	ctx context.Context,
	r io.Reader,
	cols []ImportColumn[T],
	onBatch func(ctx context.Context, rows []T) error,
	opts ...ReadOption[T],
) (ImportResult, error) {
	reader := NewReader(cols, opts...)
	return reader.Read(ctx, r, onBatch)
}
