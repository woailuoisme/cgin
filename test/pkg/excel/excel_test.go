// Package excel_test 验证 xlsx 流式写出的表头、类型规格化与公式注入防护。
package excel_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"cgin/pkg/errs"
	"cgin/pkg/excel"
)

// readSheet 解析写出的工作簿并按工作表名返回全部行。
func readSheet(t *testing.T, raw []byte, sheet string) [][]string {
	t.Helper()

	file, err := excelize.OpenReader(bytes.NewReader(raw))
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })

	rows, err := file.GetRows(sheet)
	require.NoError(t, err)
	return rows
}

// rowIterator 把静态行序列包装成 excel.RowIterator。
func rowIterator(rows [][]any, err error) excel.RowIterator {
	index := 0
	return func() ([]any, bool, error) {
		if err != nil {
			return nil, false, err
		}
		if index >= len(rows) {
			return nil, false, nil
		}
		row := rows[index]
		index++
		return row, true, nil
	}
}

func TestWriteStreamsHeaderAndRows(t *testing.T) {
	var buffer bytes.Buffer
	rows := [][]any{
		{"u1", "Alice", true, 42},
		{"u2", "Bob", false, 7},
	}

	err := excel.Write(t.Context(), &buffer, "Users",
		[]string{"ID", "Name", "Active", "Score"}, rowIterator(rows, nil))
	require.NoError(t, err)

	got := readSheet(t, buffer.Bytes(), "Users")
	require.Len(t, got, 3)
	assert.Equal(t, []string{"ID", "Name", "Active", "Score"}, got[0])
	assert.Equal(t, []string{"u1", "Alice", "Yes", "42"}, got[1])
	assert.Equal(t, []string{"u2", "Bob", "No", "7"}, got[2])
}

func TestWriteSanitizesFormulaInjection(t *testing.T) {
	var buffer bytes.Buffer
	rows := [][]any{
		{"=HYPERLINK(\"http://evil\",\"click\")"},
		{"+1+1"},
		{"-1+1"},
		{"@SUM(A1:A2)"},
	}

	require.NoError(t, excel.Write(t.Context(), &buffer, "Users",
		[]string{"Value"}, rowIterator(rows, nil)))

	got := readSheet(t, buffer.Bytes(), "Users")
	require.Len(t, got, 5)
	for i, row := range got[1:] {
		require.Len(t, row, 1)
		assert.Equal(t, "'", string(row[0][0]), "row %d should be escaped, got %q", i, row[0])
	}
}

func TestWriteFormatsTimeCells(t *testing.T) {
	moment := time.Date(2026, 10, 25, 8, 30, 0, 0, time.UTC)
	var zero time.Time

	var buffer bytes.Buffer
	rows := [][]any{
		{moment, &moment, zero, (*time.Time)(nil), "tail"},
	}
	require.NoError(t, excel.Write(t.Context(), &buffer, "", []string{"At", "Ptr", "Zero", "Nil", "Tail"}, rowIterator(rows, nil)))

	got := readSheet(t, buffer.Bytes(), "Sheet1")
	require.Len(t, got, 2)
	require.Len(t, got[1], 5)
	assert.NotEmpty(t, got[1][0])
	assert.Equal(t, got[1][0], got[1][1])
	assert.Empty(t, got[1][2])
	assert.Empty(t, got[1][3])
	assert.Equal(t, "tail", got[1][4])
}

func TestWriteEmptyIteratorKeepsHeader(t *testing.T) {
	var buffer bytes.Buffer
	require.NoError(t, excel.Write(t.Context(), &buffer, "Users",
		[]string{"ID", "Name"}, rowIterator(nil, nil)))

	got := readSheet(t, buffer.Bytes(), "Users")
	require.Len(t, got, 1)
	assert.Equal(t, []string{"ID", "Name"}, got[0])
}

func TestWritePropagatesIteratorError(t *testing.T) {
	var buffer bytes.Buffer
	cause := errors.New("database connection lost")

	err := excel.Write(t.Context(), &buffer, "Users",
		[]string{"ID"}, rowIterator(nil, cause))

	require.Error(t, err)
	assert.ErrorIs(t, err, cause)
}

func TestWriteStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var buffer bytes.Buffer
	err := excel.Write(ctx, &buffer, "Users",
		[]string{"ID"}, rowIterator([][]any{{"u1"}}, nil))

	require.ErrorIs(t, err, context.Canceled)
}

func TestWriteRejectsNilArguments(t *testing.T) {
	var buffer bytes.Buffer

	require.Error(t, excel.Write(t.Context(), nil, "Users", nil, rowIterator(nil, nil)))
	require.Error(t, excel.Write(t.Context(), &buffer, "Users", nil, nil))
}

type sampleRecord struct {
	Email string
	Name  string
	Score int
}

func sampleExportSchema() []excel.ExportColumn[sampleRecord] {
	return []excel.ExportColumn[sampleRecord]{
		{
			Header: "Email",
			Width:  25,
			Align:  excel.AlignLeft,
			Encode: func(r *sampleRecord) any { return r.Email },
		},
		{
			Header: "Name",
			Width:  20,
			Align:  excel.AlignCenter,
			Encode: func(r *sampleRecord) any { return r.Name },
		},
		{
			Header: "Score",
			Width:  10,
			Align:  excel.AlignRight,
			Encode: func(r *sampleRecord) any { return r.Score },
		},
	}
}

func sampleImportSchema() []excel.ImportColumn[sampleRecord] {
	return []excel.ImportColumn[sampleRecord]{
		{
			Header:   "Email",
			Required: true,
			Decode: func(cell string, dst *sampleRecord) error {
				if !strings.Contains(cell, "@") {
					return errors.New("invalid email")
				}
				dst.Email = strings.ToLower(cell)
				return nil
			},
		},
		{
			Header:   "Name",
			Required: true,
			Decode: func(cell string, dst *sampleRecord) error {
				dst.Name = cell
				return nil
			},
		},
		{
			Header:   "Score",
			Required: false,
			Default:  "0",
			Decode: func(cell string, dst *sampleRecord) error {
				val, err := strconv.Atoi(cell)
				if err != nil {
					return errors.New("score must be an integer")
				}
				dst.Score = val
				return nil
			},
		},
	}
}

func TestWriteTableAndReadTableRoundTrip(t *testing.T) {
	input := []sampleRecord{
		{Email: "alice@example.com", Name: "Alice", Score: 95},
		{Email: "bob@example.com", Name: "Bob", Score: 88},
		{Email: "carol@example.com", Name: "Carol", Score: 72},
	}

	var buf bytes.Buffer
	idx := 0
	err := excel.WriteTable(t.Context(), &buf, "Members", sampleExportSchema(), func() (sampleRecord, bool, error) {
		if idx >= len(input) {
			return sampleRecord{}, false, nil
		}
		item := input[idx]
		idx++
		return item, true, nil
	})
	require.NoError(t, err)

	var (
		batches [][]sampleRecord
		gotAll  []sampleRecord
	)
	result, err := excel.ReadTable(
		t.Context(),
		bytes.NewReader(buf.Bytes()),
		sampleImportSchema(),
		func(_ context.Context, rows []sampleRecord) error {
			cloned := append([]sampleRecord(nil), rows...)
			batches = append(batches, cloned)
			gotAll = append(gotAll, cloned...)
			return nil
		},
		excel.WithSheet[sampleRecord]("Members"),
		excel.WithChunkSize[sampleRecord](2),
		excel.WithUniqueKey("email", func(r *sampleRecord) string { return r.Email }),
	)
	require.NoError(t, err)
	assert.Equal(t, 3, result.TotalRows)
	assert.Equal(t, 3, result.SuccessRows)
	assert.Equal(t, 0, result.FailedRows)
	assert.False(t, result.HasErrors())
	assert.Len(t, batches, 2, "3 行数据按 chunkSize=2 应拆成 2 个批次回调")
	assert.Equal(t, input, gotAll)
}

func TestWriter_MultiSheet(t *testing.T) {
	var buf bytes.Buffer
	writer := excel.NewWriter(&buf)

	sheet1, err := excel.NewSheet(writer, "Engineering", sampleExportSchema())
	require.NoError(t, err)
	require.NoError(t, sheet1.WriteRow(&sampleRecord{Email: "dev@example.com", Name: "Dev", Score: 100}))
	require.NoError(t, sheet1.Close())

	sheet2, err := excel.NewSheet(writer, "Product", sampleExportSchema())
	require.NoError(t, err)
	require.NoError(t, sheet2.WriteRow(&sampleRecord{Email: "pm@example.com", Name: "PM", Score: 90}))
	// 故意不手动调用 sheet2.Close()，验证 writer.Close() 自动兜底 Flush
	require.NoError(t, writer.Close())

	file, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	defer func() { _ = file.Close() }()

	sheets := file.GetSheetList()
	assert.Contains(t, sheets, "Engineering")
	assert.Contains(t, sheets, "Product")

	prodRows, err := file.GetRows("Product")
	require.NoError(t, err)
	require.Len(t, prodRows, 2)
	assert.Equal(t, []string{"pm@example.com", "PM", "90"}, prodRows[1])
}

func TestReader_PartialImportWithSkipInvalid(t *testing.T) {
	var buf bytes.Buffer
	rows := [][]any{
		{"valid1@example.com", "Valid One", 10},
		{"not-an-email", "Invalid Two", 20},
		{"valid3@example.com", "Valid Three", 30},
	}
	require.NoError(t, excel.Write(t.Context(), &buf, "Sheet1", []string{"Email", "Name", "Score"}, rowIterator(rows, nil)))

	var imported []sampleRecord
	result, err := excel.ReadTable(
		t.Context(),
		bytes.NewReader(buf.Bytes()),
		sampleImportSchema(),
		func(_ context.Context, batch []sampleRecord) error {
			imported = append(imported, batch...)
			return nil
		},
		excel.WithSkipInvalid[sampleRecord](true),
	)
	require.NoError(t, err)
	assert.Equal(t, 3, result.TotalRows)
	assert.Equal(t, 2, result.SuccessRows)
	assert.Equal(t, 1, result.FailedRows)
	assert.True(t, result.HasErrors())
	require.Len(t, result.Errors, 1)
	assert.Equal(t, 3, result.Errors[0].Row)
	assert.Equal(t, "email", result.Errors[0].Field)
	require.Len(t, imported, 2)
	assert.Equal(t, "valid1@example.com", imported[0].Email)
	assert.Equal(t, "valid3@example.com", imported[1].Email)
}

func TestReader_SkipInvalidDuplicateKey(t *testing.T) {
	var buf bytes.Buffer
	rows := [][]any{
		{"user1@example.com", "User One", 10},
		{"USER1@example.com", "Duplicate User", 20}, // 重复邮箱
		{"user2@example.com", "User Two", 30},
	}
	require.NoError(t, excel.Write(t.Context(), &buf, "Sheet1", []string{"Email", "Name", "Score"}, rowIterator(rows, nil)))

	var imported []sampleRecord
	result, err := excel.ReadTable(
		t.Context(),
		bytes.NewReader(buf.Bytes()),
		sampleImportSchema(),
		func(_ context.Context, batch []sampleRecord) error {
			imported = append(imported, batch...)
			return nil
		},
		excel.WithSkipInvalid[sampleRecord](true),
		excel.WithUniqueKey("email", func(r *sampleRecord) string { return r.Email }),
	)
	require.NoError(t, err)
	assert.Equal(t, 3, result.TotalRows)
	assert.Equal(t, 2, result.SuccessRows)
	assert.Equal(t, 1, result.FailedRows)
	assert.True(t, result.HasErrors())
	require.Len(t, result.Errors, 1)
	assert.Equal(t, 3, result.Errors[0].Row)
	assert.Equal(t, "email", result.Errors[0].Field)
	assert.Contains(t, result.Errors[0].Message, "Duplicate email")

	require.Len(t, imported, 2)
	assert.Equal(t, "user1@example.com", imported[0].Email)
	assert.Equal(t, "user2@example.com", imported[1].Email)
}

func TestReadTable_ErrorCodes(t *testing.T) {
	cols := sampleImportSchema()

	t.Run("文件超限返回 EXCEL_FILE_TOO_LARGE", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, excel.Write(t.Context(), &buf, "Sheet1", []string{"Email", "Name"}, rowIterator([][]any{{"a@b.c", "A"}}, nil)))

		_, err := excel.ReadTable(
			t.Context(),
			bytes.NewReader(buf.Bytes()),
			cols,
			func(context.Context, []sampleRecord) error { return nil },
			excel.WithMaxBytes[sampleRecord](10),
		)
		require.Error(t, err)
		bizErr, ok := errs.As(err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeExcelFileTooLarge, bizErr.Code())
	})

	t.Run("损坏文件返回 EXCEL_OPEN_ERROR", func(t *testing.T) {
		_, err := excel.ReadTable(
			t.Context(),
			strings.NewReader("not-a-valid-zip-or-xlsx"),
			cols,
			func(context.Context, []sampleRecord) error { return nil },
		)
		require.Error(t, err)
		bizErr, ok := errs.As(err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeExcelOpenError, bizErr.Code())
	})

	t.Run("指定不存在的工作表返回 EMPTY_SHEETS", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, excel.Write(t.Context(), &buf, "Sheet1", []string{"Email", "Name"}, rowIterator(nil, nil)))

		_, err := excel.ReadTable(
			t.Context(),
			bytes.NewReader(buf.Bytes()),
			cols,
			func(context.Context, []sampleRecord) error { return nil },
			excel.WithSheet[sampleRecord]("NonExistent"),
		)
		require.Error(t, err)
		bizErr, ok := errs.As(err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeEmptySheets, bizErr.Code())
	})

	t.Run("缺失必填表头列返回 EXCEL_MAP_ERROR", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, excel.Write(t.Context(), &buf, "Sheet1", []string{"Email"}, rowIterator([][]any{{"a@b.c"}}, nil)))

		_, err := excel.ReadTable(
			t.Context(),
			bytes.NewReader(buf.Bytes()),
			cols,
			func(context.Context, []sampleRecord) error { return nil },
		)
		require.Error(t, err)
		bizErr, ok := errs.As(err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeExcelMapError, bizErr.Code())
		assert.Contains(t, bizErr.Fields(), "name")
	})

	t.Run("单元格校验失败返回 EXCEL_READ_ROW_ERROR 并定位行列坐标", func(t *testing.T) {
		var buf bytes.Buffer
		rows := [][]any{
			{"not-an-email", "", "not-int"},
		}
		require.NoError(t, excel.Write(t.Context(), &buf, "Sheet1", []string{"email", "name", "score"}, rowIterator(rows, nil)))

		_, err := excel.ReadTable(
			t.Context(),
			bytes.NewReader(buf.Bytes()),
			cols,
			func(context.Context, []sampleRecord) error { return nil },
		)
		require.Error(t, err)
		bizErr, ok := errs.As(err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeExcelReadRowError, bizErr.Code())

		fields := bizErr.Fields()
		assert.Contains(t, fields, "rows.2.email")
		assert.Contains(t, fields, "rows.2.name")
		assert.Contains(t, fields, "rows.2.score")
	})

	t.Run("文件内重复唯一键返回 EXCEL_DUPLICATE_ROW", func(t *testing.T) {
		var buf bytes.Buffer
		rows := [][]any{
			{"dup@example.com", "First"},
			{"DUP@example.com", "Second"},
		}
		require.NoError(t, excel.Write(t.Context(), &buf, "Sheet1", []string{"Email", "Name"}, rowIterator(rows, nil)))

		_, err := excel.ReadTable(
			t.Context(),
			bytes.NewReader(buf.Bytes()),
			cols,
			func(context.Context, []sampleRecord) error { return nil },
			excel.WithUniqueKey("email", func(r *sampleRecord) string { return r.Email }),
		)
		require.Error(t, err)
		bizErr, ok := errs.As(err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeExcelDuplicateRow, bizErr.Code())
		assert.Contains(t, bizErr.Fields(), "rows.3.email")
	})
}
