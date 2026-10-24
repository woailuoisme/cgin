// Package template 提供预设的工业级通用 PDF 凭据与报表文档模版。
package template

import (
	"context"
	"strconv"

	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/orientation"
	"github.com/johnfercher/maroto/v2/pkg/consts/pagesize"
	"github.com/samber/lo"
	"github.com/samber/oops"

	"bgin/internal/platform/i18n"
	"bgin/internal/platform/pdf"
)

// InvoiceItem 描述发票/账单明细清单项。
type InvoiceItem struct {
	Name      string
	Quantity  int
	UnitPrice string
	Amount    string
}

// InvoiceDocument 提供一份标准的企业级电子发票/结算账单 PDF 凭据实现。
type InvoiceDocument struct {
	InvoiceNumber string
	IssueDate     string
	DueDate       string
	SellerName    string
	SellerTaxID   string
	BuyerName     string
	BuyerTaxID    string
	Items         []InvoiceItem
	Subtotal      string
	Tax           string
	Total         string
	Notes         string
	PaymentQR     string
	// Language 是文案语言标签（如 zh-CN / en），留空时走 i18n 的默认语言。
	// 这里收的是原始取值，协商（Accept-Language 的 q 值、子标签退化）交给 i18n。
	Language string
}

// Title 实现 pdf.WithMetadata。
func (d *InvoiceDocument) Title() string {
	return "Invoice " + d.InvoiceNumber
}

// Author 实现 pdf.WithMetadata。
func (d *InvoiceDocument) Author() string {
	return d.SellerName
}

// Subject 实现 pdf.WithMetadata。
func (d *InvoiceDocument) Subject() string {
	return "Commercial Electronic Invoice"
}

// Keywords 实现 pdf.WithMetadata。
func (d *InvoiceDocument) Keywords() string {
	return "invoice, billing, payment"
}

// PageSize 实现 pdf.WithLayout。
func (d *InvoiceDocument) PageSize() pagesize.Type {
	return pagesize.A4
}

// Orientation 实现 pdf.WithLayout。
func (d *InvoiceDocument) Orientation() orientation.Type {
	return orientation.Vertical
}

// Margins 实现 pdf.WithLayout。
func (d *InvoiceDocument) Margins() (top, right, bottom, left float64) {
	return 12, 12, 12, 12
}

// UseDefaultFooter 实现 pdf.WithDefaultPageFooter。
func (d *InvoiceDocument) UseDefaultFooter() bool {
	return true
}

// Build 编排渲染发票主体。
//
// 所有展示文案都从 lang/<语言>/invoice.toml 取，模板里不留硬编码文案：
// 该约束是 AGENTS.md 4.5 的硬性要求，也让同一份模板能直接出中英两种凭据。
func (d *InvoiceDocument) Build(ctx context.Context, b *pdf.Builder) error {
	translator := i18n.Default()
	if translator == nil {
		return oops.Errorf("pdf: i18n translator not initialized")
	}
	text := func(key string, vars map[string]string) string {
		return translator.Invoice(d.Language, key, vars)
	}

	// 1. 文档头部：大标题与账单元信息
	b.AddTitle(text("title", nil), align.Left)
	b.AddSubtitle(text("number", map[string]string{"Number": d.InvoiceNumber}), align.Left)
	b.AddDivider()

	// 2. 交易双方元数据网格
	pairs := []pdf.KeyValuePair{
		{Key: text("seller", nil), Value: d.SellerName},
		{Key: text("buyer", nil), Value: d.BuyerName},
		{Key: text("tax_id", nil), Value: d.SellerTaxID},
		{Key: text("tax_id", nil), Value: d.BuyerTaxID},
		{Key: text("issue_date", nil), Value: d.IssueDate},
		{Key: text("due_date", nil), Value: d.DueDate},
	}
	b.AddKeyValueGrid(pairs, 2, 6.5)
	b.AddSpace(4)

	// 3. 费用清单数据表格
	tableCols := []pdf.TableColumn{
		{Header: text("item_description", nil), Size: 5, Align: align.Left},
		{Header: text("quantity", nil), Size: 2, Align: align.Center},
		{Header: text("unit_price", nil), Size: 2, Align: align.Right},
		{Header: text("amount", nil), Size: 3, Align: align.Right},
	}

	tableRows := lo.Map(d.Items, func(item InvoiceItem, _ int) []string {
		return []string{
			item.Name,
			strconv.Itoa(item.Quantity),
			item.UnitPrice,
			item.Amount,
		}
	})

	b.AddTable(pdf.TableConfig{
		Columns:      tableCols,
		Rows:         tableRows,
		HeaderHeight: 7.5,
		RowHeight:    6.5,
		Zebra:        true,
	})
	b.AddSpace(3)

	// 4. 合计汇总与支付二维码
	summaryPairs := []pdf.KeyValuePair{
		{Key: text("subtotal", nil), Value: d.Subtotal},
		{Key: text("tax", nil), Value: d.Tax},
		{Key: text("total_due", nil), Value: d.Total},
	}
	b.AddKeyValueGrid(summaryPairs, 1, 6.0)

	if d.PaymentQR != "" {
		b.AddSpace(2)
		b.AddHeading(text("payment_qr", nil), align.Left)
		b.AddQRCode(d.PaymentQR, 3, 28)
	}

	// 5. 备注说明与签章落款
	if d.Notes != "" {
		b.AddSpace(2)
		b.AddParagraph(text("notes", map[string]string{"Notes": d.Notes}))
	}

	b.AddSpace(6)
	b.AddSignature(text("signature", nil), 4, 18)

	return nil
}
