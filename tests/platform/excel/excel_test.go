// Package excel_test 验证 xlsx 流式写出的表头、类型规格化与公式注入防护。
package excel_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"cgin/internal/platform/excel"
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
