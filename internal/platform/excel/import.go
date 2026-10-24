package excel

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"

	"github.com/bits-and-blooms/bloom/v3"
	"github.com/samber/oops"
	"github.com/xuri/excelize/v2"

	"bgin/internal/platform/errs"
)

type importPipeline struct {
	service   *Service
	importObj any
	cfg       *importConfig

	headers       []string
	currentRow    int
	headingRowIdx int

	toSlice         ToSlice
	elementType     reflect.Type
	sliceRows       [][]string
	sliceRowNumbers []int

	toChunk   ToChunk
	chunkSize int
	chunkRows [][]string

	toModel   ToModel
	batchSize int
	batch     []any
	inserter  BatchInserter

	validator      WithValidation
	onRow          OnRow
	skipsOnFailure SkipsOnFailure
	canSkip        bool
	events         WithEvents

	uniqueCheck   WithUniqueCheck
	bloomFilter   *bloom.BloomFilter
	unflushedKeys map[string]struct{}
}

func newImportPipeline(service *Service, importObj any, cfg *importConfig) *importPipeline {
	p := &importPipeline{
		service:   service,
		importObj: importObj,
		cfg:       cfg,
	}

	p.toSlice, _ = importObj.(ToSlice)
	p.toModel, _ = importObj.(ToModel)
	p.toChunk, _ = importObj.(ToChunk)
	p.onRow, _ = importObj.(OnRow)
	p.uniqueCheck, _ = importObj.(WithUniqueCheck)
	p.skipsOnFailure, _ = importObj.(SkipsOnFailure)
	p.canSkip = p.skipsOnFailure != nil || (cfg != nil && cfg.skipInvalidRows)
	p.validator, _ = importObj.(WithValidation)
	p.inserter, _ = importObj.(BatchInserter)
	p.events, _ = importObj.(WithEvents)

	p.headingRowIdx = cfg.headerRowIndex
	if h, ok := importObj.(WithHeadingRow); ok {
		p.headingRowIdx = h.HeadingRow()
	}

	if p.uniqueCheck != nil {
		p.bloomFilter, p.unflushedKeys = initBloomFilter(importObj)
	}

	if p.toChunk != nil {
		p.chunkSize = defaultChunkSize
		if cr, ok := importObj.(WithChunkReading); ok && cr.ChunkSize() > 0 {
			p.chunkSize = cr.ChunkSize()
		}
		if cfg != nil && cfg.batchSize > 0 {
			p.chunkSize = cfg.batchSize
		}
		p.chunkRows = make([][]string, 0, p.chunkSize)
	}

	if p.toModel != nil {
		p.batchSize = 1
		if b, ok := importObj.(WithBatchInserts); ok && b.BatchSize() > 0 {
			p.batchSize = b.BatchSize()
		} else if cr, ok := importObj.(WithChunkReading); ok && cr.ChunkSize() > 0 {
			p.batchSize = cr.ChunkSize()
		}
		if cfg != nil && cfg.batchSize > 0 {
			p.batchSize = cfg.batchSize
		}
	}

	return p
}

func (p *importPipeline) recordFailure(result *ImportResult, failure RowFailure) {
	p.recordFailureWithCount(result, failure, 1)
}

func (p *importPipeline) recordFailureWithCount(result *ImportResult, failure RowFailure, count int) {
	result.Failures = append(result.Failures, failure)
	result.SkippedCount += count
	if p.skipsOnFailure != nil {
		p.skipsOnFailure.OnFailure(failure)
	}
}

// Run 执行导入管线，流式解析数据并按契约派发，返回汇总结果与详细失败报告。
func (p *importPipeline) Run(ctx context.Context, r io.Reader) (*ImportResult, error) {
	if ms, ok := p.importObj.(WithMultipleSheetsImport); ok {
		return p.runMultipleSheets(ctx, ms, r)
	}

	if err := p.prepareSliceTarget(); err != nil {
		return nil, err
	}

	f, cleanup, err := openExcelSafely(r, p.cfg, p.service.tempPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
		cleanup()
	}()

	sheetName, err := resolveSheetName(f, p.cfg.sheetName)
	if err != nil {
		return nil, err
	}

	return p.runWithFile(ctx, f, sheetName)
}

func (p *importPipeline) runMultipleSheets(ctx context.Context, ms WithMultipleSheetsImport, r io.Reader) (*ImportResult, error) {
	f, cleanup, err := openExcelSafely(r, p.cfg, p.service.tempPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
		cleanup()
	}()

	result := &ImportResult{}
	for sheetName, handlerObj := range ms.Sheets() {
		subCfg := *p.cfg
		subCfg.sheetName = sheetName

		subPipeline := newImportPipeline(p.service, handlerObj, &subCfg)
		if subPipeline.toSlice != nil {
			if err := subPipeline.prepareSliceTarget(); err != nil {
				return result, err
			}
		}

		subRes, err := subPipeline.runWithFile(ctx, f, sheetName)
		if err != nil {
			return result, err
		}
		result.merge(subRes)
	}

	return result, nil
}

func (p *importPipeline) runWithFile(ctx context.Context, f *excelize.File, sheetName string) (*ImportResult, error) {
	if err := p.initPreloadKeys(ctx); err != nil {
		return nil, err
	}

	rows, err := f.Rows(sheetName)
	if err != nil {
		return nil, fmt.Errorf("failed to read sheet %s: %w", sheetName, err)
	}
	defer func() { _ = rows.Close() }()

	result := &ImportResult{}
	if err = p.scanRows(ctx, rows, result); err != nil {
		return result, err
	}

	if err = p.flushChunk(ctx, result); err != nil {
		return result, err
	}

	if err = p.flushModels(ctx); err != nil {
		return result, err
	}

	if err = p.writeSliceTarget(result); err != nil {
		return result, err
	}

	return result, nil
}

func openExcelSafely(r io.Reader, cfg *importConfig, tempDir string) (*excelize.File, func(), error) {
	opts := excelize.Options{
		UnzipSizeLimit:    cfg.unzipSizeLimit,
		UnzipXMLSizeLimit: cfg.unzipXMLSizeLimit,
	}
	maxLimit := cmp.Or(cfg.maxFileSize, defaultMaxFileSize)

	if file, ok := r.(*os.File); ok {
		fi, statErr := file.Stat()
		if statErr == nil && fi.Size() > maxLimit {
			return nil, nil, oops.Code(errs.CodeExcelFileTooLarge).
				Errorf("excel file size %d bytes exceeds limit %d bytes", fi.Size(), maxLimit)
		}

		f, err := excelize.OpenFile(file.Name(), opts)
		if err != nil {
			return nil, nil, oops.Code(errs.CodeExcelOpenError).Wrap(err)
		}
		return f, func() {}, nil
	}

	threshold := cmp.Or(cfg.memorySpoolThreshold, defaultMemorySpoolThreshold)
	limitedReader := io.LimitReader(r, maxLimit+1)
	buf := make([]byte, threshold+1)
	n, readErr := io.ReadFull(limitedReader, buf)
	if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
		if int64(n) > maxLimit {
			return nil, nil, oops.Code(errs.CodeExcelFileTooLarge).
				Errorf("excel stream size %d bytes exceeds limit of %d bytes", n, maxLimit)
		}

		f, openErr := excelize.OpenReader(bytes.NewReader(buf[:n]), opts)
		if openErr != nil {
			return nil, nil, oops.Code(errs.CodeExcelOpenError).Wrap(openErr)
		}
		return f, func() {}, nil
	}
	if readErr != nil {
		return nil, nil, oops.Code(errs.CodeExcelReadError).Wrap(readErr)
	}

	return spoolToDiskAndOpen(buf[:n], limitedReader, maxLimit, opts, tempDir)
}

func spoolToDiskAndOpen(initialBuf []byte, remaining io.Reader, maxLimit int64, opts excelize.Options, tempDir string) (*excelize.File, func(), error) {
	if err := os.MkdirAll(tempDir, 0o750); err != nil {
		tempDir = os.TempDir()
	}

	tmpFile, err := os.CreateTemp(tempDir, "bgin-excel-spool-*.xlsx")
	if err != nil {
		return nil, nil, oops.Code(errs.CodeExcelTempFileError).Wrap(err)
	}

	success := false
	cleanup := func() { _ = os.Remove(tmpFile.Name()) }
	defer func() {
		if !success {
			_ = tmpFile.Close()
			cleanup()
		}
	}()

	if _, err = tmpFile.Write(initialBuf); err != nil {
		return nil, nil, oops.Code(errs.CodeExcelSpoolWriteError).Wrap(err)
	}

	written, copyErr := io.Copy(tmpFile, remaining)
	if copyErr != nil {
		return nil, nil, oops.Code(errs.CodeExcelSpoolCopyError).Wrap(copyErr)
	}

	totalBytes := int64(len(initialBuf)) + written
	if totalBytes > maxLimit {
		return nil, nil, oops.Code(errs.CodeExcelFileTooLarge).
			Errorf("excel stream size exceeds max file size limit of %d bytes", maxLimit)
	}

	if err = tmpFile.Close(); err != nil {
		return nil, nil, oops.Code(errs.CodeExcelSpoolCloseError).Wrap(err)
	}

	f, openErr := excelize.OpenFile(tmpFile.Name(), opts)
	if openErr != nil {
		return nil, nil, oops.Code(errs.CodeExcelOpenError).Wrap(openErr)
	}

	success = true
	return f, cleanup, nil
}

func (p *importPipeline) prepareSliceTarget() error {
	if p.toSlice == nil {
		return nil
	}

	destVal := reflect.ValueOf(p.toSlice.ToSlice())
	if destVal.Kind() != reflect.Pointer || destVal.Elem().Kind() != reflect.Slice {
		return fmt.Errorf("excel: ToSlice() must return a pointer to a slice, got %T", p.toSlice.ToSlice())
	}

	p.elementType = destVal.Elem().Type().Elem()
	if p.elementType.Kind() == reflect.Pointer {
		p.elementType = p.elementType.Elem()
	}

	return nil
}

func (p *importPipeline) scanRowColumns(rows *excelize.Rows, result *ImportResult) ([]string, bool, error) {
	cols, err := rows.Columns()
	if err == nil {
		return cols, false, nil
	}
	if !p.canSkip {
		return nil, false, fmt.Errorf("failed to scan row %d: %w", p.currentRow, err)
	}
	p.recordFailure(result, RowFailure{
		Row:   p.currentRow,
		Error: fmt.Errorf("failed to scan row columns: %w", err),
	})
	return nil, true, nil
}

func (p *importPipeline) scanRows(ctx context.Context, rows *excelize.Rows, result *ImportResult) error {
	for rows.Next() {
		p.currentRow++

		cols, skipped, err := p.scanRowColumns(rows, result)
		if err != nil {
			return err
		}
		if skipped || len(cols) == 0 || isRowEmpty(cols) {
			continue
		}

		if p.headingRowIdx > 0 && p.currentRow < p.headingRowIdx {
			continue
		}

		if p.headingRowIdx > 0 && p.currentRow == p.headingRowIdx {
			p.headers = slices.Clone(cols)
			continue
		}

		if p.cfg.maxRows > 0 && result.TotalRows >= p.cfg.maxRows {
			break
		}

		result.TotalRows++

		if p.uniqueCheck != nil {
			isDup, dupErr := p.checkUnique(ctx, cols, result)
			if dupErr != nil {
				return dupErr
			}
			if isDup {
				continue
			}
		}

		if err = p.processRow(ctx, cols, result); err != nil {
			return err
		}
	}

	return nil
}

func (p *importPipeline) processRow(ctx context.Context, cols []string, result *ImportResult) error {
	switch {
	case p.onRow != nil:
		if err := p.onRow.OnRow(cols); err != nil {
			if p.canSkip {
				p.recordFailure(result, RowFailure{
					Row:    p.currentRow,
					Values: cols,
					Error:  err,
				})
				return nil
			}
			return fmt.Errorf("failed to process row %d in OnRow: %w", p.currentRow, err)
		}
		result.ImportedCount++

	case p.toChunk != nil:
		p.chunkRows = append(p.chunkRows, cols)
		if len(p.chunkRows) >= p.chunkSize {
			if err := p.flushChunk(ctx, result); err != nil {
				return err
			}
		}

	case p.toModel != nil:
		return p.processModelRow(ctx, cols, result)

	case p.toSlice != nil:
		p.sliceRows = append(p.sliceRows, cols)
		p.sliceRowNumbers = append(p.sliceRowNumbers, p.currentRow)
	}

	if p.cfg.onProgress != nil {
		p.cfg.onProgress(result.ImportedCount, result.TotalRows)
	}
	if p.events != nil {
		p.events.OnProgress(result.ImportedCount, result.TotalRows)
	}

	return nil
}

func (p *importPipeline) flushChunk(ctx context.Context, result *ImportResult) error {
	if p.toChunk == nil || len(p.chunkRows) == 0 {
		return nil
	}

	chunkData := p.chunkRows
	p.chunkRows = make([][]string, 0, p.chunkSize)

	if err := p.toChunk.Chunk(ctx, chunkData); err != nil {
		if p.canSkip {
			p.recordFailureWithCount(result, RowFailure{
				Row:   p.currentRow,
				Error: fmt.Errorf("chunk processing failed: %w", err),
			}, len(chunkData))
			return nil
		}
		return fmt.Errorf("failed to process chunk at row %d: %w", p.currentRow, err)
	}

	if len(p.unflushedKeys) > 0 {
		clear(p.unflushedKeys)
	}

	result.ImportedCount += len(chunkData)
	return nil
}

func (p *importPipeline) processModelRow(ctx context.Context, cols []string, result *ImportResult) error {
	model, err := p.toModel.ToModel(cols)
	if err != nil {
		if p.canSkip {
			p.recordFailure(result, RowFailure{
				Row:    p.currentRow,
				Values: cols,
				Error:  err,
			})
			return nil
		}
		return fmt.Errorf("failed to convert row %d to model: %w", p.currentRow, err)
	}

	valid, valErr := p.validateTarget(model, p.currentRow, cols, result)
	if valErr != nil || !valid {
		return valErr
	}

	p.batch = append(p.batch, model)
	result.ImportedCount++

	return p.tryFlushBatch(ctx)
}

func (p *importPipeline) flushBatch(ctx context.Context) error {
	if p.inserter == nil || len(p.batch) == 0 {
		return nil
	}
	if err := p.inserter.InsertBatch(ctx, p.batch); err != nil {
		return fmt.Errorf("failed to insert batch of models: %w", err)
	}
	p.batch = nil
	if len(p.unflushedKeys) > 0 {
		clear(p.unflushedKeys)
	}
	return nil
}

func (p *importPipeline) tryFlushBatch(ctx context.Context) error {
	if len(p.batch) >= p.batchSize {
		return p.flushBatch(ctx)
	}
	return nil
}

func (p *importPipeline) flushModels(ctx context.Context) error {
	return p.flushBatch(ctx)
}

func (p *importPipeline) validateTarget(
	target any,
	rowNum int,
	cols []string,
	result *ImportResult,
) (bool, error) {
	if p.validator == nil {
		return true, nil
	}

	if err := p.validator.Validate(target); err != nil {
		if p.canSkip {
			p.recordFailure(result, RowFailure{
				Row:    rowNum,
				Values: cols,
				Error:  err,
			})
			return false, nil
		}
		return false, fmt.Errorf("row %d validation failed: %w", rowNum, err)
	}

	return true, nil
}

func (p *importPipeline) writeSliceTarget(result *ImportResult) error {
	if p.toSlice == nil || len(p.sliceRows) == 0 {
		return nil
	}

	destVal := reflect.ValueOf(p.toSlice.ToSlice()).Elem()
	isPtrSlice := destVal.Type().Elem().Kind() == reflect.Pointer
	sliceElements := make([]reflect.Value, 0, len(p.sliceRows))

	for idx, row := range p.sliceRows {
		rowNum := p.sliceRowNumbers[idx]
		newElemPtr := reflect.New(p.elementType)
		newElem := newElemPtr.Elem()

		if err := mapRowToStruct(row, p.headers, newElem); err != nil {
			if p.canSkip {
				p.recordFailure(result, RowFailure{
					Row:    rowNum,
					Values: row,
					Error:  err,
				})
				continue
			}
			return err
		}

		valid, valErr := p.validateTarget(newElemPtr.Interface(), rowNum, row, result)
		if valErr != nil {
			return valErr
		}
		if !valid {
			continue
		}

		result.ImportedCount++
		if isPtrSlice {
			sliceElements = append(sliceElements, newElemPtr)
		} else {
			sliceElements = append(sliceElements, newElem)
		}
	}

	resultSlice := reflect.MakeSlice(destVal.Type(), len(sliceElements), len(sliceElements))
	for i, el := range sliceElements {
		resultSlice.Index(i).Set(el)
	}

	destVal.Set(resultSlice)
	return nil
}

func (p *importPipeline) initPreloadKeys(ctx context.Context) error {
	if p.uniqueCheck == nil || p.bloomFilter == nil {
		return nil
	}

	if pk, ok := p.importObj.(WithPreloadKeys); ok {
		keys, err := pk.PreloadKeys(ctx)
		if err != nil {
			return oops.Code(errs.CodeExcelReadError).In("excel.PreloadKeys").Wrap(err)
		}
		for _, k := range keys {
			if k != "" {
				p.bloomFilter.AddString(k)
			}
		}
	}

	return nil
}

func (p *importPipeline) checkUnique(ctx context.Context, cols []string, result *ImportResult) (bool, error) {
	if p.uniqueCheck == nil || p.bloomFilter == nil {
		return false, nil
	}

	key := p.uniqueCheck.UniqueKey(cols)
	if key == "" {
		return false, nil
	}

	if p.bloomFilter.TestString(key) {
		isDup, err := p.isKeyDuplicate(ctx, key)
		if err != nil {
			return false, err
		}
		if isDup {
			return p.handleDuplicateRow(cols, key, result)
		}
	}

	p.bloomFilter.AddString(key)
	if p.unflushedKeys != nil {
		p.unflushedKeys[key] = struct{}{}
	}
	return false, nil
}

func (p *importPipeline) isKeyDuplicate(ctx context.Context, key string) (bool, error) {
	if _, ok := p.unflushedKeys[key]; ok {
		return true, nil
	}
	exists, err := p.uniqueCheck.ExistsDB(ctx, key)
	if err != nil {
		return false, oops.Code(errs.CodeExcelReadRowError).
			In("excel.isKeyDuplicate").
			Errorf("failed to verify duplicate key %q at row %d: %w", key, p.currentRow, err)
	}
	return exists, nil
}

func (p *importPipeline) handleDuplicateRow(cols []string, key string, result *ImportResult) (bool, error) {
	dupErr := oops.Code(errs.CodeExcelDuplicateRow).
		In("excel.handleDuplicateRow").
		Errorf("duplicate entry for key %q at row %d", key, p.currentRow)

	if !p.canSkip {
		return true, dupErr
	}

	p.recordFailure(result, RowFailure{
		Row:    p.currentRow,
		Values: cols,
		Field:  key,
		Error:  dupErr,
	})
	return true, nil
}

func initBloomFilter(importObj any) (*bloom.BloomFilter, map[string]struct{}) {
	expectedItems := uint(1_000_000)
	fpRate := 0.0001
	if uc, ok := importObj.(WithUniqueConfig); ok {
		items, rate := uc.UniqueConfig()
		if items > 0 {
			expectedItems = items
		}
		if rate > 0 && rate < 1 {
			fpRate = rate
		}
	}
	return bloom.NewWithEstimates(expectedItems, fpRate), make(map[string]struct{})
}
