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

func sampleSchema() []excel.Column[sampleRecord] {
	return []excel.Column[sampleRecord]{
		{
			Header:   "Email",
			Required: true,
			Encode:   func(r *sampleRecord) any { return r.Email },
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
			Encode:   func(r *sampleRecord) any { return r.Name },
			Decode: func(cell string, dst *sampleRecord) error {
				dst.Name = cell
				return nil
			},
		},
		{
			Header:   "Score",
			Required: false,
			Encode:   func(r *sampleRecord) any { return r.Score },
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
	err := excel.WriteTable(t.Context(), &buf, "Members", sampleSchema(), func() (sampleRecord, bool, error) {
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
	err = excel.ReadTable(
		t.Context(),
		bytes.NewReader(buf.Bytes()),
		sampleSchema(),
		func(_ context.Context, rows []sampleRecord) error {
			cloned := append([]sampleRecord(nil), rows...)
			batches = append(batches, cloned)
			gotAll = append(gotAll, cloned...)
			return nil
		},
		excel.WithSheet[sampleRecord]("Members"),
		excel.WithChunkSize[sampleRecord](2),
		excel.WithUniqueKey[sampleRecord]("email", func(r *sampleRecord) string { return r.Email }),
	)
	require.NoError(t, err)
	assert.Len(t, batches, 2, "3 行数据按 chunkSize=2 应拆成 2 个批次回调")
	assert.Equal(t, input, gotAll)
}

func TestReadTable_ErrorCodes(t *testing.T) {
	cols := sampleSchema()

	t.Run("文件超限返回 EXCEL_FILE_TOO_LARGE", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, excel.Write(t.Context(), &buf, "Sheet1", []string{"Email", "Name"}, rowIterator([][]any{{"a@b.c", "A"}}, nil)))

		err := excel.ReadTable(
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
		err := excel.ReadTable(
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

		err := excel.ReadTable(
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

		err := excel.ReadTable(
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

		err := excel.ReadTable(
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

		err := excel.ReadTable(
			t.Context(),
			bytes.NewReader(buf.Bytes()),
			cols,
			func(context.Context, []sampleRecord) error { return nil },
			excel.WithUniqueKey[sampleRecord]("email", func(r *sampleRecord) string { return r.Email }),
		)
		require.Error(t, err)
		bizErr, ok := errs.As(err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeExcelDuplicateRow, bizErr.Code())
		assert.Contains(t, bizErr.Fields(), "rows.3.email")
	})
}
