// Package pdf 提供基于 Maroto v2 的现代化、企业级 PDF 报表与凭据生成服务。
package pdf

import (
	"github.com/johnfercher/maroto/v2/pkg/consts/pagesize"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// DeliveryMode 定义 HTTP 响应的交付模式。
type DeliveryMode string

const (
	// DeliveryInline 浏览器在线预览 (Content-Disposition: inline)。
	DeliveryInline DeliveryMode = "inline"
	// DeliveryAttachment 强制浏览器下载 (Content-Disposition: attachment)。
	DeliveryAttachment DeliveryMode = "attachment"
)

// 标准色彩主题 Token（符合现代化企业级视觉设计系统）。
var (
	// ColorPrimary 品牌主色 (Deep Indigo: #1E40AF)。
	ColorPrimary = props.Color{Red: 30, Green: 64, Blue: 175}
	// ColorSecondary 品牌辅助色 (Sky Blue: #3B82F6)。
	ColorSecondary = props.Color{Red: 59, Green: 130, Blue: 246}
	// ColorTextPrimary 正文主文字颜色 (Charcoal: #1F2937)。
	ColorTextPrimary = props.Color{Red: 31, Green: 41, Blue: 55}
	// ColorTextSecondary 次要说明文字颜色 (Slate: #4B5563)。
	ColorTextSecondary = props.Color{Red: 75, Green: 85, Blue: 99}
	// ColorTextMuted 弱化注释与页脚文字颜色 (Gray: #9CA3AF)。
	ColorTextMuted = props.Color{Red: 156, Green: 163, Blue: 175}
	// ColorBgHeader 表格表头背景底色 (Light Gray: #F3F4F6)。
	ColorBgHeader = props.Color{Red: 243, Green: 244, Blue: 246}
	// ColorBgZebra 斑马纹行交替底色 (Very Light Gray: #F9FAFB)。
	ColorBgZebra = props.Color{Red: 249, Green: 250, Blue: 251}
	// ColorBorder 边框与分割线颜色 (Border Gray: #E5E7EB)。
	ColorBorder = props.Color{Red: 229, Green: 231, Blue: 235}
	// ColorWhite 纯白颜色。
	ColorWhite = props.Color{Red: 255, Green: 255, Blue: 255}
	// ColorSuccess 成功/已支付状态色 (Emerald: #059669)。
	ColorSuccess = props.Color{Red: 5, Green: 150, Blue: 105}
	// ColorWarning 警告/待处理状态色 (Amber: #D97706)。
	ColorWarning = props.Color{Red: 217, Green: 119, Blue: 6}
	// ColorDanger 危险/已驳回状态色 (Rose: #DC2626)。
	ColorDanger = props.Color{Red: 220, Green: 38, Blue: 38}
)

// 标准排版字号阶梯规范 (单位: pt)。
const (
	// FontSizeTitle 顶层文档大标题 (16pt)。
	FontSizeTitle = 16.0
	// FontSizeSubtitle 章节副标题 (12pt)。
	FontSizeSubtitle = 12.0
	// FontSizeHeading 栏目与分组标题 (10.5pt)。
	FontSizeHeading = 10.5
	// FontSizeBody 正文与单元格内容字号 (9.0pt)。
	FontSizeBody = 9.0
	// FontSizeCaption 说明、注解与标签字号 (8.0pt)。
	FontSizeCaption = 8.0
	// FontSizeSmall 页眉页脚及版权信息微型字号 (7.0pt)。
	FontSizeSmall = 7.0
)

// 页面布局默认规范。
const (
	// DefaultMargin 默认页边距 (10mm)。
	DefaultMargin = 10.0
	// DefaultPageSize 默认纸张尺寸 (A4)。
	DefaultPageSize = pagesize.A4
)
