package excel

// AlignMode 描述单元格内容水平对齐方式。
type AlignMode string

const (
	AlignLeft   AlignMode = "left"
	AlignCenter AlignMode = "center"
	AlignRight  AlignMode = "right"
)

// ExportColumn 描述强类型表格单列的流式导出契约（纯函数式投影，零反射）。
type ExportColumn[T any] struct {
	// Header 表头展示名。
	Header string
	// Width 建议列宽（字符单位）；未指定或 <=0 时使用自适应默认宽度。
	Width float64
	// Align 单元格水平对齐方式（默认左对齐，数值/金额推荐右对齐，日期/状态推荐居中）。
	Align AlignMode
	// Encode 将领域实体投影为单元格安全值的纯函数。
	Encode func(row *T) any
}

// ImportColumn 描述强类型表格单列的流式导入解析契约。
type ImportColumn[T any] struct {
	// Header 表头展示名（导入匹配时大小写不敏感且自动去除首尾空白）。
	Header string
	// Required 是否必填列（表头必须存在，且非空单元格）。
	Required bool
	// Default 单元格为空或缺失时的回退默认值。
	Default string
	// Decode 将单元格清洗文本解析为领域实体字段的纯函数。
	Decode func(cell string, dst *T) error
}
