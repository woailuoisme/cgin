package vector

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"slices"
	"strings"

	"github.com/pgvector/pgvector-go"
)

// Vector 表示高维浮点数特征向量，用于 AI Embedding 与空间相似度搜索。
type Vector struct {
	inner pgvector.Vector
}

// NewVector 从 float32 切片构造特征向量。
// 对入参切片执行防御性拷贝，杜绝调用方后续改动底层切片影响向量状态。
func NewVector(values []float32) Vector {
	return Vector{inner: pgvector.NewVector(slices.Clone(values))}
}

// newVectorNoClone 从已在包内独占分配的 float32 切片构造特征向量（跳过克隆）。
func newVectorNoClone(values []float32) Vector {
	return Vector{inner: pgvector.NewVector(values)}
}

// NewVectorFromFloat64 从 float64 切片构造特征向量（自动降精转换为 float32）。
func NewVectorFromFloat64(values []float64) Vector {
	if values == nil {
		return Vector{}
	}
	f32s := make([]float32, len(values))
	for i, item := range values {
		f32s[i] = float32(item)
	}
	return newVectorNoClone(f32s)
}

// Parse 从向量格式字符串（如 "[1.0, 2.0, 3.0]"）解析出 Vector 实例。
func Parse(s string) (Vector, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	var inner pgvector.Vector
	if err := inner.Parse(s); err != nil {
		return Vector{}, fmt.Errorf("vector: parse failed: %w", err)
	}
	return Vector{inner: inner}, nil
}

// Dim 返回向量的维度（即元素个数）。
func (v Vector) Dim() int {
	return len(v.rawSlice())
}

// At 返回指定索引位置的向量元素分量（O(1) 复杂度，零内存分配）。若索引越界将触发运行时 panic。
func (v Vector) At(i int) float32 {
	return v.rawSlice()[i]
}

// Get 安全获取指定索引位置的向量分量。若索引越界或向量为空，返回 (0, false)；否则返回对应分量值与 true。
func (v Vector) Get(i int) (float32, bool) {
	s := v.rawSlice()
	if i < 0 || i >= len(s) {
		return 0, false
	}
	return s[i], true
}

// Slice 返回向量的底层 float32 切片副本。
// 对底层切片执行防御性克隆，防止外部篡改向量数据。
func (v Vector) Slice() []float32 {
	return slices.Clone(v.inner.Slice())
}

// rawSlice 返回底层原始切片（仅限包内只读数学计算使用，避免多余堆克隆）。
func (v Vector) rawSlice() []float32 {
	return v.inner.Slice()
}

// IsEmpty 判定向量是否为空（维度为 0）。
func (v Vector) IsEmpty() bool {
	return v.Dim() == 0
}

// String 输出向量的标准文本表示（如 "[1,2,3]"）。
func (v Vector) String() string {
	return v.inner.String()
}

// Value 实现 driver.Valuer 接口，将向量输出为数据库驱动支持的值。
//
//nolint:nilnil // driver.Valuer returns (nil, nil) to write SQL NULL
func (v Vector) Value() (driver.Value, error) {
	if v.IsEmpty() {
		return nil, nil
	}
	return v.inner.Value()
}

// Scan 实现 sql.Scanner 接口，从数据库返回值中反序列化向量。
func (v *Vector) Scan(src any) error {
	if src == nil {
		v.inner = pgvector.Vector{}
		return nil
	}
	switch s := src.(type) {
	case string:
		src = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	case []byte:
		src = strings.ReplaceAll(strings.TrimSpace(string(s)), " ", "")
	}
	return v.inner.Scan(src)
}

// MarshalJSON 实现 json.Marshaler 接口，序列化为标准 JSON 浮点数组。
func (v Vector) MarshalJSON() ([]byte, error) {
	if v.IsEmpty() {
		return []byte("[]"), nil
	}
	return v.inner.MarshalJSON()
}

// UnmarshalJSON 实现 json.Unmarshaler 接口，宽容解析 JSON 数组。
func (v *Vector) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("[]")) {
		v.inner = pgvector.Vector{}
		return nil
	}
	return v.inner.UnmarshalJSON(data)
}

// IsZero 判定当前向量是否为零值（空向量）。
func (v Vector) IsZero() bool {
	return v.IsEmpty()
}

// DecodeBinary 实现 pgx/v5 驱动的二进制协议解码接口。
func (v *Vector) DecodeBinary(buf []byte) error {
	return v.inner.DecodeBinary(buf)
}

// EncodeBinary 实现 pgx/v5 驱动的二进制协议编码接口。
func (v Vector) EncodeBinary(buf []byte) ([]byte, error) {
	return v.inner.EncodeBinary(buf)
}
