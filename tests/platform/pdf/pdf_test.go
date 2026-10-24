package pdf_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/orientation"
	"github.com/johnfercher/maroto/v2/pkg/consts/pagesize"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/i18n"
	"bgin/internal/platform/pdf"
	"bgin/internal/platform/pdf/template"
	"bgin/internal/platform/storage"
)

type simpleTestDoc struct {
	title   string
	content string
}

func (d *simpleTestDoc) Build(ctx context.Context, b *pdf.Builder) error {
	b.AddTitle(d.title, align.Left)
	b.AddSubtitle("Subtitle Testing", align.Left)
	b.AddHeading("Section 1: Details", align.Left)
	b.AddParagraph(d.content, align.Left)
	b.AddDivider()

	pairs := []pdf.KeyValuePair{
		{Key: "Environment", Value: "Testing"},
		{Key: "Version", Value: "1.0.0"},
	}
	b.AddKeyValueGrid(pairs, 2)
	b.AddSpace(3)

	b.AddTable(pdf.TableConfig{
		Columns: []pdf.TableColumn{
			{Header: "Item", Size: 6, Align: align.Left},
			{Header: "Value", Size: 6, Align: align.Right},
		},
		Rows: [][]string{
			{"Row 1", "100"},
			{"Row 2", "200"},
		},
		Zebra: true,
	})

	b.AddQRCode("https://example.com/verify/123", 3, 25)
	b.AddSignature("Authorized Signature", 4, 15)
	return nil
}

type fullFeatureDoc struct {
	simpleTestDoc
}

func (d *fullFeatureDoc) Title() string                               { return "Full Feature Report" }
func (d *fullFeatureDoc) Author() string                              { return "System Test" }
func (d *fullFeatureDoc) Subject() string                             { return "Automated Quality Report" }
func (d *fullFeatureDoc) Keywords() string                            { return "pdf, test, report" }
func (d *fullFeatureDoc) PageSize() pagesize.Type                     { return pagesize.A4 }
func (d *fullFeatureDoc) Orientation() orientation.Type               { return orientation.Vertical }
func (d *fullFeatureDoc) Margins() (top, right, bottom, left float64) { return 10, 10, 10, 10 }
func (d *fullFeatureDoc) UseDefaultFooter() bool                      { return true }

func TestRenderSimpleDocument(t *testing.T) {
	ctx := t.Context()
	doc := &simpleTestDoc{
		title:   "Test Document",
		content: "This is a test paragraph verifying PDF generation.",
	}

	svc := pdf.NewService(config.PDFConfig{
		FontFamily: "Helvetica",
	}, nil, nil)

	data, err := svc.Render(ctx, doc)
	require.NoError(t, err)
	assert.NotEmpty(t, data)
	assert.True(t, bytes.HasPrefix(data, []byte("%PDF")), "PDF bytes must start with %PDF header")
}

func TestRenderToWriter(t *testing.T) {
	ctx := t.Context()
	doc := &simpleTestDoc{
		title:   "Writer Document",
		content: "Testing io.Writer pipeline.",
	}

	svc := pdf.NewService(config.PDFConfig{
		FontFamily: "Helvetica",
	}, nil, nil)

	var buf bytes.Buffer
	err := svc.RenderToWriter(ctx, doc, &buf)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(buf.Bytes(), []byte("%PDF")))
}

func TestStreamToHTTP(t *testing.T) {
	svc := pdf.NewService(config.PDFConfig{
		FontFamily: "Helvetica",
	}, nil, nil)

	t.Run("inline preview mode", func(t *testing.T) {
		w := httptest.NewRecorder()

		doc := &simpleTestDoc{title: "Preview", content: "Inline content"}
		err := svc.StreamToHTTP(t.Context(), w, "preview.pdf", doc, pdf.DeliveryInline)
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))
		assert.Contains(t, w.Header().Get("Content-Disposition"), "inline")
		assert.Contains(t, w.Header().Get("Content-Disposition"), "preview.pdf")
		assert.True(t, bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF")))
	})

	t.Run("attachment download mode", func(t *testing.T) {
		w := httptest.NewRecorder()

		doc := &simpleTestDoc{title: "Download", content: "Attachment content"}
		err := svc.StreamToHTTP(t.Context(), w, "账单.pdf", doc, pdf.DeliveryAttachment)
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Disposition"), "attachment")
		assert.Contains(t, w.Header().Get("Content-Disposition"), "UTF-8''")
		assert.True(t, bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF")))
	})

	t.Run("standard http response writer mode", func(t *testing.T) {
		w := httptest.NewRecorder()

		doc := &simpleTestDoc{title: "StandardHTTP", content: "Standard HTTP content"}
		err := svc.StreamToHTTP(t.Context(), w, "std.pdf", doc, pdf.DeliveryInline)
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))
		assert.Contains(t, w.Header().Get("Content-Disposition"), "inline")
		assert.Contains(t, w.Header().Get("Content-Disposition"), "std.pdf")
		assert.True(t, bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF")))
	})
}

func TestSaveToStorage(t *testing.T) {
	ctx := t.Context()
	disk, err := storage.Open(ctx, config.StorageConfig{
		Driver: config.StorageDriverLocal,
		Local:  config.LocalStorageConfig{Root: t.TempDir()},
	})
	require.NoError(t, err)

	svc := pdf.NewService(config.PDFConfig{
		FontFamily: "Helvetica",
	}, disk, nil)

	doc := &simpleTestDoc{title: "Storage Test", content: "Saving to disk test"}
	storagePath := "invoices/2026/09/inv-001.pdf"

	savedPath, err := svc.SaveToStorage(ctx, storagePath, doc)
	require.NoError(t, err)
	assert.Equal(t, storagePath, savedPath)

	savedBytes, err := disk.Get(ctx, storagePath)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(savedBytes, []byte("%PDF")))
}

func TestFontFallbackToHelvetica(t *testing.T) {
	ctx := t.Context()
	// 配置不存在的字体路径
	svc := pdf.NewService(config.PDFConfig{
		FontPath:   "./non_existent_fonts_dir/missing.ttf",
		FontFamily: "CustomMissingFont",
	}, nil, nil)

	doc := &simpleTestDoc{title: "Fallback Test", content: "Fallback content"}
	data, err := svc.Render(ctx, doc)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(data, []byte("%PDF")))
}

func TestDocumentWithConcerns(t *testing.T) {
	ctx := t.Context()
	doc := &fullFeatureDoc{
		simpleTestDoc: simpleTestDoc{
			title:   "Full Report",
			content: "Verifying metadata and layout concerns.",
		},
	}

	svc := pdf.NewService(config.PDFConfig{
		FontFamily: "Helvetica",
	}, nil, nil)

	data, err := svc.Render(ctx, doc)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(data, []byte("%PDF")))
}

// TestInvoiceDocumentTemplate 校验发票模板在中英两种语言下都能渲染出合法 PDF。
//
// 展示文案取自 lang/<语言>/invoice.toml，故渲染前必须先注册默认翻译器；
// 文案本体的正确性由 tests/platform/i18n 的用例锁定，这里只负责"语言这条路能走通"。
func TestInvoiceDocumentTemplate(t *testing.T) {
	ctx := t.Context()

	translator, err := i18n.New(&config.Config{I18n: config.I18nConfig{DefaultLanguage: "zh-CN"}})
	require.NoError(t, err)
	i18n.SetDefault(translator)

	newInvoice := func(language string) *template.InvoiceDocument {
		return &template.InvoiceDocument{
			InvoiceNumber: "INV-20260928-001",
			IssueDate:     "2026-09-28",
			DueDate:       "2026-10-28",
			SellerName:    "Cloud Platform Technology Co., Ltd.",
			SellerTaxID:   "91310000XXXXXXXXXX",
			BuyerName:     "Acme International Corp",
			BuyerTaxID:    "91110000YYYYYYYYYY",
			Items: []template.InvoiceItem{
				{Name: "Enterprise Cloud Hosting (Annual)", Quantity: 1, UnitPrice: "$12,000.00", Amount: "$12,000.00"},
				{Name: "Dedicated Support SLA", Quantity: 12, UnitPrice: "$500.00", Amount: "$6,000.00"},
				{Name: "Storage Add-on (10TB)", Quantity: 2, UnitPrice: "$1,200.00", Amount: "$2,400.00"},
			},
			Subtotal:  "$20,400.00",
			Tax:       "$1,224.00",
			Total:     "$21,624.00",
			Notes:     "Please remit payment within 30 days of invoice date.",
			PaymentQR: "https://pay.example.com/invoice/INV-20260928-001",
			Language:  language,
		}
	}

	svc := pdf.NewService(config.PDFConfig{
		FontFamily: "Helvetica",
	}, nil, nil)

	for _, language := range []string{"zh-CN", "en", ""} {
		t.Run("language="+language, func(t *testing.T) {
			pdfBytes, err := svc.Render(ctx, newInvoice(language))
			require.NoError(t, err)
			assert.NotEmpty(t, pdfBytes)
			assert.True(t, bytes.HasPrefix(pdfBytes, []byte("%PDF")))
		})
	}
}

// TestInvoiceRequiresTranslator 校验未注册翻译器时给出明确错误，而不是渲染出一份空标签凭据。
func TestInvoiceRequiresTranslator(t *testing.T) {
	i18n.SetDefault(nil)

	svc := pdf.NewService(config.PDFConfig{FontFamily: "Helvetica"}, nil, nil)
	_, err := svc.Render(t.Context(), &template.InvoiceDocument{InvoiceNumber: "INV-001"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "translator not initialized")
}

func TestRegisterPDFServiceInstance(t *testing.T) {
	ctx := t.Context()
	cfg := &config.Config{
		PDF: config.PDFConfig{
			FontFamily: "Helvetica",
		},
	}

	service, err := pdf.RegisterPDF(cfg, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, service)

	doc := &simpleTestDoc{title: "RegisterPDF Service", content: "Testing instance invocation"}
	data, err := service.Render(ctx, doc)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(data, []byte("%PDF")))
}

// TestComponentAndDeliveryMethods 验证可复用 Component 渲染与附件/内联两种交付方式。
func TestComponentAndDeliveryMethods(t *testing.T) {
	ctx := t.Context()

	svc := pdf.NewService(config.PDFConfig{
		FontFamily: "Helvetica",
	}, nil, nil)

	// 定义测试组件
	headerComp := pdf.ComponentFunc(func(ctx context.Context, b *pdf.Builder) error {
		b.AddHeading("Reusable Header Component", align.Center)
		return nil
	})

	doc := &simpleTestDoc{title: "Component Test", content: "Testing reusable component"}
	// 测试直接调用 AddComponent
	builderDoc := &componentDoc{
		comp: headerComp,
		doc:  doc,
	}

	data, err := svc.Render(ctx, builderDoc)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(data, []byte("%PDF")))

	// 测试附件下载与内联预览两种交付方式
	recDown := httptest.NewRecorder()
	err = svc.StreamToHTTP(ctx, recDown, "report.pdf", doc, pdf.DeliveryAttachment)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, recDown.Code)
	assert.Contains(t, recDown.Header().Get("Content-Disposition"), "attachment")

	recInline := httptest.NewRecorder()
	err = svc.StreamToHTTP(ctx, recInline, "preview.pdf", doc, pdf.DeliveryInline)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, recInline.Code)
	assert.Contains(t, recInline.Header().Get("Content-Disposition"), "inline")
}

type componentDoc struct {
	comp pdf.Component
	doc  pdf.Document
}

func (d *componentDoc) Build(ctx context.Context, b *pdf.Builder) error {
	if err := b.AddComponent(ctx, d.comp); err != nil {
		return err
	}
	return d.doc.Build(ctx, b)
}
