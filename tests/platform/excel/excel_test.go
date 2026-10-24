// Package excel_test 验证 Excel 与 CSV 流式导入导出与 Concern 契约。
package excel_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/excel"
)

type sampleUser struct {
	ID    string `excel:"用户编号"`
	Name  string `excel:"姓名"`
	Email string `excel:"邮箱"`
}

type userExport struct {
	users []sampleUser
}

func (u *userExport) FromSlice() any {
	return u.users
}

func (u *userExport) Headings() []string {
	return []string{"ID", "姓名", "邮箱"}
}

func (u *userExport) Mapping(row any) []any {
	user, ok := row.(sampleUser)
	if !ok {
		return nil
	}
	return []any{user.ID, user.Name, user.Email}
}

func (u *userExport) Title() string {
	return "用户列表"
}

func TestExportAndImportMemorySlice(t *testing.T) {
	ctx := t.Context()

	users := []sampleUser{
		{ID: "1", Name: "Alex", Email: "alex@example.com"},
		{ID: "2", Name: "Bob", Email: "bob@example.com"},
	}

	// 1. 测试导出
	var buf bytes.Buffer
	exportObj := &userExport{users: users}
	err := excel.Export(ctx, exportObj, &buf)
	require.NoError(t, err)
	assert.NotEmpty(t, buf.Bytes())

	// 2. 验证导出的 Excel 文件结构
	f, err := excelize.OpenReader(&buf)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	sheetName := "用户列表"
	rows, err := f.GetRows(sheetName)
	require.NoError(t, err)
	require.Len(t, rows, 3) // 表头 + 2行数据

	assert.Equal(t, []string{"ID", "姓名", "邮箱"}, rows[0])
	assert.Equal(t, []string{"1", "Alex", "alex@example.com"}, rows[1])
	assert.Equal(t, []string{"2", "Bob", "bob@example.com"}, rows[2])
}

type userImportRowCollector struct {
	rows [][]string
}

func (c *userImportRowCollector) OnRow(row []string) error {
	c.rows = append(c.rows, row)
	return nil
}

func TestImportRows(t *testing.T) {
	ctx := t.Context()

	// 构造待导入的 Excel 文件
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := "Sheet1"
	require.NoError(t, f.SetSheetRow(sheet, "A1", &[]any{"ID", "姓名", "邮箱"}))
	require.NoError(t, f.SetSheetRow(sheet, "A2", &[]any{"101", "Charlie", "charlie@example.com"}))
	require.NoError(t, f.SetSheetRow(sheet, "A3", &[]any{"102", "David", "david@example.com"}))

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	// 执行导入
	collector := &userImportRowCollector{}
	err := excel.Import(ctx, collector, &buf, excel.WithHeadingRowIndex(1))
	require.NoError(t, err)
	require.Len(t, collector.rows, 2)
	assert.Equal(t, []string{"101", "Charlie", "charlie@example.com"}, collector.rows[0])
	assert.Equal(t, []string{"102", "David", "david@example.com"}, collector.rows[1])
}

func TestExportSliceAndCSV(t *testing.T) {
	ctx := t.Context()

	items := []sampleUser{
		{ID: "u_1", Name: "Elena", Email: "elena@example.com"},
	}
	headings := []string{"ID", "Name", "Email"}
	mapper := func(u sampleUser) []any {
		return []any{u.ID, u.Name, u.Email}
	}

	// 1. 流式导出为 Excel
	var excelBuf bytes.Buffer
	err := excel.ExportSlice(ctx, &excelBuf, items, headings, mapper)
	require.NoError(t, err)
	assert.NotEmpty(t, excelBuf.Bytes())

	// 2. 流式导出为 CSV
	var csvBuf bytes.Buffer
	err = excel.ExportSliceCSV(ctx, &csvBuf, items, headings, mapper)
	require.NoError(t, err)

	csvContent := csvBuf.String()
	assert.Contains(t, csvContent, "ID,Name,Email")
	assert.Contains(t, csvContent, "u_1,Elena,elena@example.com")
}

func TestNilGuard(t *testing.T) {
	ctx := t.Context()

	require.Error(t, excel.Export(ctx, nil, nil))
	require.Error(t, excel.Import(ctx, nil, nil))
}

type sampleDept struct {
	ID   string `excel:"部门编号"`
	Name string `excel:"部门名称"`
}

type deptExport struct {
	depts []sampleDept
}

func (d *deptExport) FromSlice() any {
	return d.depts
}

func (d *deptExport) Headings() []string {
	return []string{"部门编号", "部门名称"}
}

func (d *deptExport) Mapping(row any) []any {
	dept, ok := row.(sampleDept)
	if !ok {
		return nil
	}
	return []any{dept.ID, dept.Name}
}

func (d *deptExport) Title() string {
	return "部门列表"
}

type multiSheetExport struct {
	users []sampleUser
	depts []sampleDept
}

func (m *multiSheetExport) Sheets() []any {
	return []any{
		&userExport{users: m.users},
		&deptExport{depts: m.depts},
	}
}

func TestMultipleSheetsExport(t *testing.T) {
	ctx := t.Context()

	exportObj := &multiSheetExport{
		users: []sampleUser{
			{ID: "u1", Name: "Alice", Email: "alice@test.com"},
		},
		depts: []sampleDept{
			{ID: "d1", Name: "Engineering"},
		},
	}

	var buf bytes.Buffer
	err := excel.Export(ctx, exportObj, &buf)
	require.NoError(t, err)

	f, err := excelize.OpenReader(&buf)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	sheetList := f.GetSheetList()
	assert.Contains(t, sheetList, "用户列表")
	assert.Contains(t, sheetList, "部门列表")

	userRows, err := f.GetRows("用户列表")
	require.NoError(t, err)
	require.Len(t, userRows, 2)
	assert.Equal(t, []string{"ID", "姓名", "邮箱"}, userRows[0])
	assert.Equal(t, []string{"u1", "Alice", "alice@test.com"}, userRows[1])

	deptRows, err := f.GetRows("部门列表")
	require.NoError(t, err)
	require.Len(t, deptRows, 2)
	assert.Equal(t, []string{"部门编号", "部门名称"}, deptRows[0])
	assert.Equal(t, []string{"d1", "Engineering"}, deptRows[1])
}

type faultyUserImport struct {
	failures []excel.RowFailure
	models   []sampleUser
}

func (f *faultyUserImport) HeadingRow() int {
	return 1
}

func (f *faultyUserImport) ToModel(row []string) (any, error) {
	if len(row) < 3 {
		return nil, assert.AnError
	}
	if row[0] == "bad" {
		return nil, assert.AnError
	}
	u := sampleUser{ID: row[0], Name: row[1], Email: row[2]}
	f.models = append(f.models, u)
	return u, nil
}

func (f *faultyUserImport) OnFailure(failure excel.RowFailure) {
	f.failures = append(f.failures, failure)
}

func TestSkipsOnFailureAndImportResult(t *testing.T) {
	ctx := t.Context()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := "Sheet1"
	require.NoError(t, f.SetSheetRow(sheet, "A1", &[]any{"ID", "Name", "Email"}))
	require.NoError(t, f.SetSheetRow(sheet, "A2", &[]any{"101", "Good1", "good1@example.com"}))
	require.NoError(t, f.SetSheetRow(sheet, "A3", &[]any{"bad", "Faulty", "faulty@example.com"}))
	require.NoError(t, f.SetSheetRow(sheet, "A4", &[]any{"103", "Good2", "good2@example.com"}))

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	imp := &faultyUserImport{}
	result, err := excel.ImportWithResult(ctx, imp, &buf)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 3, result.TotalRows)
	assert.Equal(t, 2, result.ImportedCount)
	assert.Equal(t, 1, result.SkippedCount)
	assert.True(t, result.HasFailures())
	require.Len(t, result.Failures, 1)
	assert.Equal(t, 3, result.Failures[0].Row)
	assert.Equal(t, "bad", result.Failures[0].Values[0])

	require.Len(t, imp.failures, 1)
	assert.Equal(t, 3, imp.failures[0].Row)
	require.Len(t, imp.models, 2)
}

type formulaExport struct {
	rows [][]any
}

func (e *formulaExport) FromSlice() any {
	return e.rows
}

func (e *formulaExport) Headings() []string {
	return []string{"FormulaText", "SafeRawFormula"}
}

func (e *formulaExport) Mapping(row any) []any {
	if r, ok := row.([]any); ok {
		return r
	}
	return nil
}

func TestFormulaInjectionSanitization(t *testing.T) {
	ctx := t.Context()

	exportObj := &formulaExport{
		rows: [][]any{
			{"=SUM(A1:A10)", excel.RawFormula("=SUM(A1:A10)")},
			{"+cmd|' /C calc'!A0", "@calc"},
			{"-5+2", "=1+1"},
		},
	}

	var buf bytes.Buffer
	err := excel.Export(ctx, exportObj, &buf)
	require.NoError(t, err)

	f, err := excelize.OpenReader(&buf)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	rows, err := f.GetRows("Sheet1")
	require.NoError(t, err)
	require.Len(t, rows, 4)

	// 行 2：危险前缀 = 被转义为 '=SUM(...)，而 RawFormula 保持原样
	assert.Equal(t, "'=SUM(A1:A10)", rows[1][0])
	assert.Equal(t, "=SUM(A1:A10)", rows[1][1])

	// 行 3：+ 与 @ 均被转义
	assert.Equal(t, "'+cmd|' /C calc'!A0", rows[2][0])
	assert.Equal(t, "'@calc", rows[2][1])

	// 行 4：- 与 = 均被转义
	assert.Equal(t, "'-5+2", rows[3][0])
	assert.Equal(t, "'=1+1", rows[3][1])
}

func TestHeaderStyleAndFreezePanes(t *testing.T) {
	ctx := t.Context()

	items := []sampleUser{
		{ID: "1", Name: "John", Email: "john@example.com"},
	}

	var buf bytes.Buffer
	err := excel.ExportSlice(
		ctx,
		&buf,
		items,
		[]string{"ID", "Name", "Email"},
		func(u sampleUser) []any {
			return []any{u.ID, u.Name, u.Email}
		},
		excel.WithFreezeHeader(true),
		excel.WithHeaderStyleOption(excel.DefaultHeaderStyle()),
	)
	require.NoError(t, err)

	f, err := excelize.OpenReader(&buf)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	panes, err := f.GetPanes("Sheet1")
	require.NoError(t, err)
	assert.True(t, panes.Freeze)
	assert.Equal(t, 1, panes.YSplit)
}

type multiSheetImporter struct {
	userCollector *userImportRowCollector
	deptCollector *userImportRowCollector
}

func (m *multiSheetImporter) Sheets() map[string]any {
	return map[string]any{
		"Users": m.userCollector,
		"Depts": m.deptCollector,
	}
}

func TestWithMultipleSheetsImport(t *testing.T) {
	ctx := t.Context()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	_, _ = f.NewSheet("Users")
	_, _ = f.NewSheet("Depts")
	_ = f.DeleteSheet("Sheet1")

	require.NoError(t, f.SetSheetRow("Users", "A1", &[]any{"u101", "Tom"}))
	require.NoError(t, f.SetSheetRow("Depts", "A1", &[]any{"d201", "Sales"}))

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	userCol := &userImportRowCollector{}
	deptCol := &userImportRowCollector{}
	importer := &multiSheetImporter{
		userCollector: userCol,
		deptCollector: deptCol,
	}

	result, err := excel.ImportWithResult(ctx, importer, &buf, excel.WithHeadingRowIndex(0))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2, result.TotalRows)
	assert.Equal(t, 2, result.ImportedCount)

	require.Len(t, userCol.rows, 1)
	assert.Equal(t, []string{"u101", "Tom"}, userCol.rows[0])

	require.Len(t, deptCol.rows, 1)
	assert.Equal(t, []string{"d201", "Sales"}, deptCol.rows[0])
}

func TestImportToModelsGeneric(t *testing.T) {
	ctx := t.Context()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := "Sheet1"
	require.NoError(t, f.SetSheetRow(sheet, "A1", &[]any{"ID", "姓名", "邮箱"}))
	require.NoError(t, f.SetSheetRow(sheet, "A2", &[]any{"1", "Sam", "sam@example.com"}))
	require.NoError(t, f.SetSheetRow(sheet, "A3", &[]any{"2", "Lucy", "lucy@example.com"}))

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	models, result, err := excel.ImportToModels(
		ctx,
		&buf,
		func(row []string, rowIdx int) (sampleUser, error) {
			return sampleUser{ID: row[0], Name: row[1], Email: row[2]}, nil
		},
		excel.WithHeadingRowIndex(1),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2, result.TotalRows)
	assert.Equal(t, 2, result.ImportedCount)
	require.Len(t, models, 2)
	assert.Equal(t, "Sam", models[0].Name)
	assert.Equal(t, "Lucy", models[1].Name)
}

func TestExportBuilder(t *testing.T) {
	ctx := t.Context()

	users := []sampleUser{
		{ID: "b1", Name: "BuilderBob", Email: "bob@builder.com"},
	}

	// 1. 测试 Excel 导出
	var excelBuf bytes.Buffer
	err := excel.NewExportBuilder[sampleUser]().
		FromSlice(users).
		WithHeadings("ID", "Name", "Email").
		WithMapping(func(u sampleUser) []any {
			return []any{u.ID, u.Name, u.Email}
		}).
		Write(ctx, &excelBuf)
	require.NoError(t, err)
	assert.NotEmpty(t, excelBuf.Bytes())

	// 2. 测试 CSV 导出
	var csvBuf bytes.Buffer
	err = excel.NewExportBuilder[sampleUser]().
		FromSlice(users).
		WithHeadings("ID", "Name", "Email").
		WithMapping(func(u sampleUser) []any {
			return []any{u.ID, u.Name, u.Email}
		}).
		WriteCSV(ctx, &csvBuf)
	require.NoError(t, err)
	assert.Contains(t, csvBuf.String(), "BuilderBob")
}

type batchInsertCollector struct {
	batches [][]any
}

func (b *batchInsertCollector) HeadingRow() int {
	return 1
}

func (b *batchInsertCollector) BatchSize() int {
	return 2
}

func (b *batchInsertCollector) ToModel(row []string) (any, error) {
	return sampleUser{ID: row[0], Name: row[1], Email: row[2]}, nil
}

func (b *batchInsertCollector) InsertBatch(ctx context.Context, batch []any) error {
	copied := make([]any, len(batch))
	copy(copied, batch)
	b.batches = append(b.batches, copied)
	return nil
}

func TestBatchInserter(t *testing.T) {
	ctx := t.Context()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := "Sheet1"
	require.NoError(t, f.SetSheetRow(sheet, "A1", &[]any{"ID", "Name", "Email"}))
	require.NoError(t, f.SetSheetRow(sheet, "A2", &[]any{"1", "User1", "u1@test.com"}))
	require.NoError(t, f.SetSheetRow(sheet, "A3", &[]any{"2", "User2", "u2@test.com"}))
	require.NoError(t, f.SetSheetRow(sheet, "A4", &[]any{"3", "User3", "u3@test.com"}))

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	inserter := &batchInsertCollector{}
	result, err := excel.ImportWithResult(ctx, inserter, &buf)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 3, result.TotalRows)
	assert.Equal(t, 3, result.ImportedCount)

	require.Len(t, inserter.batches, 2)
	assert.Len(t, inserter.batches[0], 2)
	assert.Len(t, inserter.batches[1], 1)
}

type chunkCollector struct {
	chunks [][][]string
}

func (c *chunkCollector) HeadingRow() int {
	return 1
}

func (c *chunkCollector) ChunkSize() int {
	return 2
}

func (c *chunkCollector) Chunk(_ context.Context, rows [][]string) error {
	copied := make([][]string, len(rows))
	for i, r := range rows {
		rowCopy := make([]string, len(r))
		copy(rowCopy, r)
		copied[i] = rowCopy
	}
	c.chunks = append(c.chunks, copied)
	return nil
}

func TestToChunkAndWithChunkReading(t *testing.T) {
	ctx := t.Context()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := "Sheet1"
	require.NoError(t, f.SetSheetRow(sheet, "A1", &[]any{"ID", "Name"}))
	require.NoError(t, f.SetSheetRow(sheet, "A2", &[]any{"1", "A"}))
	require.NoError(t, f.SetSheetRow(sheet, "A3", &[]any{"2", "B"}))
	require.NoError(t, f.SetSheetRow(sheet, "A4", &[]any{"3", "C"}))
	require.NoError(t, f.SetSheetRow(sheet, "A5", &[]any{"4", "D"}))
	require.NoError(t, f.SetSheetRow(sheet, "A6", &[]any{"5", "E"}))

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	collector := &chunkCollector{}
	result, err := excel.ImportWithResult(ctx, collector, &buf)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 5, result.TotalRows)
	assert.Equal(t, 5, result.ImportedCount)

	require.Len(t, collector.chunks, 3) // 2 + 2 + 1
	assert.Len(t, collector.chunks[0], 2)
	assert.Len(t, collector.chunks[1], 2)
	assert.Len(t, collector.chunks[2], 1)
}

type userSliceImporter struct {
	users *[]sampleUser
}

func (u *userSliceImporter) HeadingRow() int {
	return 1
}

func (u *userSliceImporter) ToSlice() any {
	return u.users
}

func TestHybridSpoolerThreshold(t *testing.T) {
	ctx := t.Context()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := "Sheet1"
	require.NoError(t, f.SetSheetRow(sheet, "A1", &[]any{"ID", "Name"}))
	require.NoError(t, f.SetSheetRow(sheet, "A2", &[]any{"1", "SpoolUser"}))

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	// 设置内存阈值为 50 字节，强制让 Excel 数据流溢出到磁盘临时文件
	var users []sampleUser
	importObj := &userSliceImporter{users: &users}

	err := excel.Import(ctx, importObj, &buf, excel.WithMemorySpoolThreshold(50))
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "1", users[0].ID)
	assert.Equal(t, "SpoolUser", users[0].Name)
}

func TestMaxFileSizeLimit(t *testing.T) {
	ctx := t.Context()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := "Sheet1"
	require.NoError(t, f.SetSheetRow(sheet, "A1", &[]any{"ID", "Name"}))
	require.NoError(t, f.SetSheetRow(sheet, "A2", &[]any{"1", "LargeUser"}))

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	// 设置最大文件大小为 10 字节，预期触发文件过大错误
	var users []sampleUser
	importObj := &userSliceImporter{users: &users}

	err := excel.Import(ctx, importObj, &buf, excel.WithMaxFileSize(10))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "excel stream size")

	// 该失败是"客户端把文件改小就能成功"的一类，必须映射为 413 而非 500，
	// 否则调用方拿不到可据以处理的错误码（见 errs/codes.go 的子系统错误码登记）。
	status, resp := errs.Map(err)
	assert.Equal(t, http.StatusRequestEntityTooLarge, status)
	assert.Equal(t, "EXCEL_FILE_TOO_LARGE", resp.Code)
	assert.NotEqual(t, "An unexpected error occurred", resp.Message)
}

type mockChunkedQueryExport struct {
	items []sampleUser
}

func (m *mockChunkedQueryExport) ChunkSize() int {
	return 2
}

func (m *mockChunkedQueryExport) Headings() []string {
	return []string{"ID", "Name", "Email"}
}

func (m *mockChunkedQueryExport) Mapping(row any) []any {
	u, ok := row.(sampleUser)
	if !ok {
		return nil
	}
	return []any{u.ID, u.Name, u.Email}
}

func (m *mockChunkedQueryExport) FetchChunk(_ context.Context, offset int, limit int) ([]any, error) {
	if offset >= len(m.items) {
		return nil, nil
	}
	end := min(offset+limit, len(m.items))

	result := make([]any, end-offset)
	for i, item := range m.items[offset:end] {
		result[i] = item
	}
	return result, nil
}

func TestFromChunkedQueryExport(t *testing.T) {
	ctx := t.Context()

	items := []sampleUser{
		{ID: "1", Name: "Chunk1", Email: "c1@test.com"},
		{ID: "2", Name: "Chunk2", Email: "c2@test.com"},
		{ID: "3", Name: "Chunk3", Email: "c3@test.com"},
		{ID: "4", Name: "Chunk4", Email: "c4@test.com"},
		{ID: "5", Name: "Chunk5", Email: "c5@test.com"},
	}

	exportObj := &mockChunkedQueryExport{items: items}

	// 1. 测试 Excel 格式
	var excelBuf bytes.Buffer
	err := excel.Export(ctx, exportObj, &excelBuf)
	require.NoError(t, err)

	f, err := excelize.OpenReader(&excelBuf)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	rows, err := f.GetRows("Sheet1")
	require.NoError(t, err)
	require.Len(t, rows, 6) // 表头 + 5行
	assert.Equal(t, []string{"ID", "Name", "Email"}, rows[0])
	assert.Equal(t, []string{"5", "Chunk5", "c5@test.com"}, rows[5])

	// 2. 测试 CSV 格式
	var csvBuf bytes.Buffer
	err = excel.Export(ctx, exportObj, &csvBuf, excel.WithSheetName("data.csv"))
	require.NoError(t, err)
	assert.Contains(t, csvBuf.String(), "Chunk1")
	assert.Contains(t, csvBuf.String(), "Chunk5")
}

func TestMaxExportRowsAndProgress(t *testing.T) {
	ctx := t.Context()

	items := []sampleUser{
		{ID: "1", Name: "U1", Email: "u1@test.com"},
		{ID: "2", Name: "U2", Email: "u2@test.com"},
		{ID: "3", Name: "U3", Email: "u3@test.com"},
		{ID: "4", Name: "U4", Email: "u4@test.com"},
		{ID: "5", Name: "U5", Email: "u5@test.com"},
	}

	exportObj := &mockChunkedQueryExport{items: items}

	var progressReports []int
	var excelBuf bytes.Buffer

	// 限制最多导出 3 行
	err := excel.Export(
		ctx, exportObj, &excelBuf,
		excel.WithMaxExportRows(3),
		excel.WithExportProgress(func(processedRows int) {
			progressReports = append(progressReports, processedRows)
		}),
	)
	require.NoError(t, err)

	f, err := excelize.OpenReader(&excelBuf)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	rows, err := f.GetRows("Sheet1")
	require.NoError(t, err)
	require.Len(t, rows, 4) // 表头 + 3行
	assert.Equal(t, []string{"1", "U1", "u1@test.com"}, rows[1])
	assert.Equal(t, []string{"3", "U3", "u3@test.com"}, rows[3])
	assert.NotEmpty(t, progressReports)
}

type uniqueUserImport struct {
	importedRows [][]string
	existingDB   map[string]bool
	failures     []excel.RowFailure
	customConfig bool
}

func (u *uniqueUserImport) OnRow(row []string) error {
	u.importedRows = append(u.importedRows, row)
	return nil
}

func (u *uniqueUserImport) UniqueKey(row []string) string {
	if len(row) > 0 {
		return row[0] // 使用第 1 列作为唯一键 (ID)
	}
	return ""
}

func (u *uniqueUserImport) ExistsDB(_ context.Context, key string) (bool, error) {
	return u.existingDB[key], nil
}

func (u *uniqueUserImport) OnFailure(failure excel.RowFailure) {
	u.failures = append(u.failures, failure)
}

func (u *uniqueUserImport) UniqueConfig() (expectedItems uint, falsePositiveRate float64) {
	if u.customConfig {
		return 50000, 0.0005
	}
	return 1000000, 0.0001
}

type preloadingUserImport struct {
	uniqueUserImport
	preloadList []string
}

func (p *preloadingUserImport) PreloadKeys(_ context.Context) ([]string, error) {
	return p.preloadList, nil
}

func TestImportWithUniqueCheck_DuplicatesSkipped(t *testing.T) {
	ctx := t.Context()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := "Sheet1"
	require.NoError(t, f.SetSheetRow(sheet, "A1", &[]any{"ID", "姓名", "邮箱"}))
	require.NoError(t, f.SetSheetRow(sheet, "A2", &[]any{"101", "Alice", "alice@example.com"}))
	require.NoError(t, f.SetSheetRow(sheet, "A3", &[]any{"102", "Bob", "bob@example.com"}))
	require.NoError(t, f.SetSheetRow(sheet, "A4", &[]any{"101", "Alice Dup", "alice_dup@example.com"})) // 重复 ID 101
	require.NoError(t, f.SetSheetRow(sheet, "A5", &[]any{"103", "Carol", "carol@example.com"}))

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	importer := &uniqueUserImport{
		existingDB: make(map[string]bool),
	}

	result, err := excel.ImportWithResult(ctx, importer, &buf)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 4, result.TotalRows)
	assert.Equal(t, 3, result.ImportedCount)
	assert.Equal(t, 1, result.SkippedCount)
	require.Len(t, result.Failures, 1)

	failure := result.Failures[0]
	assert.Equal(t, 4, failure.Row)
	assert.Equal(t, "101", failure.Field)
	assert.Contains(t, failure.Error.Error(), "duplicate entry for key \"101\"")

	// 确认导入的行不包含重复行
	require.Len(t, importer.importedRows, 3)
	assert.Equal(t, "101", importer.importedRows[0][0])
	assert.Equal(t, "102", importer.importedRows[1][0])
	assert.Equal(t, "103", importer.importedRows[2][0])
}

func TestImportWithUniqueCheck_AbortOnDuplicateWhenNoSkip(t *testing.T) {
	ctx := t.Context()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := "Sheet1"
	require.NoError(t, f.SetSheetRow(sheet, "A1", &[]any{"ID", "姓名", "邮箱"}))
	require.NoError(t, f.SetSheetRow(sheet, "A2", &[]any{"201", "Alice", "alice@example.com"}))
	require.NoError(t, f.SetSheetRow(sheet, "A3", &[]any{"201", "Alice Dup", "alice@example.com"})) // 重复

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	strict := &strictUniqueImporter{
		db: make(map[string]bool),
	}

	err := excel.Import(ctx, strict, &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate entry for key \"201\"")
}

type strictUniqueImporter struct {
	imported [][]string
	db       map[string]bool
}

func (s *strictUniqueImporter) OnRow(row []string) error {
	s.imported = append(s.imported, row)
	return nil
}

func (s *strictUniqueImporter) UniqueKey(row []string) string {
	if len(row) > 0 {
		return row[0]
	}
	return ""
}

func (s *strictUniqueImporter) ExistsDB(_ context.Context, key string) (bool, error) {
	return s.db[key], nil
}

func TestImportWithUniqueCheck_PreloadKeys(t *testing.T) {
	ctx := t.Context()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := "Sheet1"
	require.NoError(t, f.SetSheetRow(sheet, "A1", &[]any{"ID", "姓名", "邮箱"}))
	require.NoError(t, f.SetSheetRow(sheet, "A2", &[]any{"DB_EXISTS_1", "Old User", "old@example.com"}))
	require.NoError(t, f.SetSheetRow(sheet, "A3", &[]any{"NEW_USER_2", "New User", "new@example.com"}))

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	importer := &preloadingUserImport{
		uniqueUserImport: uniqueUserImport{
			existingDB: map[string]bool{
				"DB_EXISTS_1": true,
			},
		},
		preloadList: []string{"DB_EXISTS_1"},
	}

	result, err := excel.ImportWithResult(ctx, importer, &buf)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 2, result.TotalRows)
	assert.Equal(t, 1, result.ImportedCount)
	assert.Equal(t, 1, result.SkippedCount)
	require.Len(t, result.Failures, 1)
	assert.Equal(t, "DB_EXISTS_1", result.Failures[0].Field)
	assert.Equal(t, "NEW_USER_2", importer.importedRows[0][0])
}
