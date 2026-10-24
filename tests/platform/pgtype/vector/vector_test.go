package vector_test

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"bgin/internal/platform/pgtype/vector"
)

// TestVector_Basic 验证向量的基本构造、维度、切片导出与字符串解析。
func TestVector_Basic(t *testing.T) {
	v := vector.NewVector([]float32{1.0, 2.0, 3.0})
	assert.Equal(t, 3, v.Dim())
	assert.False(t, v.IsEmpty())
	assert.Equal(t, []float32{1.0, 2.0, 3.0}, v.Slice())
	assert.Equal(t, "[1,2,3]", v.String())

	// 验证 At(i) 元素分量读取与越界防护
	assert.InDelta(t, 1.0, float64(v.At(0)), 1e-6)
	assert.InDelta(t, 2.0, float64(v.At(1)), 1e-6)
	assert.InDelta(t, 3.0, float64(v.At(2)), 1e-6)
	assert.Panics(t, func() { v.At(3) })
	assert.Panics(t, func() { v.At(-1) })

	// 验证 Get(i) 安全访问器与边界防守
	val, ok := v.Get(0)
	assert.True(t, ok)
	assert.InDelta(t, 1.0, float64(val), 1e-6)
	val, ok = v.Get(2)
	assert.True(t, ok)
	assert.InDelta(t, 3.0, float64(val), 1e-6)
	_, ok = v.Get(3)
	assert.False(t, ok)
	_, ok = v.Get(-1)
	assert.False(t, ok)

	// 从 float64 构造
	v64 := vector.NewVectorFromFloat64([]float64{1.1, 2.2})
	assert.Equal(t, 2, v64.Dim())
	assert.InDelta(t, 1.1, float64(v64.Slice()[0]), 1e-5)
	assert.InDelta(t, 2.2, float64(v64.Slice()[1]), 1e-5)

	// 零值与空向量
	var emptyVec vector.Vector
	assert.True(t, emptyVec.IsEmpty())
	assert.Equal(t, 0, emptyVec.Dim())
	assert.Empty(t, emptyVec.Slice())

	// 文本解析
	parsed, err := vector.Parse("[4.0, 5.0, 6.0]")
	require.NoError(t, err)
	assert.Equal(t, 3, parsed.Dim())
	assert.Equal(t, []float32{4.0, 5.0, 6.0}, parsed.Slice())

	// 非法文本解析
	_, err = vector.Parse("invalid-vector")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector: parse failed")

	// 验证 Uber Go Style Guide 防御性拷贝隔离
	orig := []float32{10.0, 20.0}
	vDef := vector.NewVector(orig)
	orig[0] = 999.0
	assert.InDelta(t, float32(10.0), vDef.Slice()[0], 1e-6, "修改原始入参切片不应影响 Vector 内部状态")

	exported := vDef.Slice()
	exported[0] = 888.0
	assert.InDelta(t, float32(10.0), vDef.Slice()[0], 1e-6, "修改导出的 Slice 切片不应影响 Vector 内部状态")
}

// TestVector_ScannerValuer 验证与 PostgreSQL 数据库驱动的接口交互。
func TestVector_ScannerValuer(t *testing.T) {
	v := vector.NewVector([]float32{1.5, 2.5})

	// Valuer
	val, err := v.Value()
	require.NoError(t, err)
	assert.NotNil(t, val)

	// 空向量 Value 产出 SQL NULL (nil)
	var empty vector.Vector
	emptyVal, err := empty.Value()
	require.NoError(t, err)
	assert.Nil(t, emptyVal)

	// Scanner
	var scanned vector.Vector
	err = scanned.Scan("[1.5, 2.5]")
	require.NoError(t, err)
	assert.Equal(t, 2, scanned.Dim())
	assert.InDelta(t, 1.5, float64(scanned.Slice()[0]), 1e-5)

	// Scan nil 清空
	err = scanned.Scan(nil)
	require.NoError(t, err)
	assert.True(t, scanned.IsEmpty())
}

// TestVector_JSON 验证标准 JSON 数组的序列化与反序列化。
func TestVector_JSON(t *testing.T) {
	v := vector.NewVector([]float32{10.0, 20.0})

	// Marshal
	bytes, err := json.Marshal(v)
	require.NoError(t, err)
	assert.Equal(t, "[10,20]", string(bytes))

	// Unmarshal
	var unmarshaled vector.Vector
	err = json.Unmarshal([]byte("[10.0, 20.0]"), &unmarshaled)
	require.NoError(t, err)
	assert.Equal(t, 2, unmarshaled.Dim())
	assert.InDelta(t, 10.0, float64(unmarshaled.Slice()[0]), 1e-5)

	// 反序列化 null / 空
	var nullVec vector.Vector
	err = json.Unmarshal([]byte("null"), &nullVec)
	require.NoError(t, err)
	assert.True(t, nullVec.IsEmpty())

	// 反序列化空数组 "[]" 及带空格数组
	var emptyArrVec vector.Vector
	err = json.Unmarshal([]byte("  []  "), &emptyArrVec)
	require.NoError(t, err)
	assert.True(t, emptyArrVec.IsEmpty())
}

// TestVector_Calc 验证纯 Go 内存向量距离与相似度数学真值。
func TestVector_Calc(t *testing.T) {
	// 正交单位向量：点积为 0，余弦相似度为 0，余弦距离为 1
	v1 := vector.NewVector([]float32{1.0, 0.0, 0.0})
	v2 := vector.NewVector([]float32{0.0, 1.0, 0.0})

	dot, err := vector.DotProduct(v1, v2)
	require.NoError(t, err)
	assert.InDelta(t, 0.0, dot, 1e-6)

	sim, err := vector.CosineSimilarity(v1, v2)
	require.NoError(t, err)
	assert.InDelta(t, 0.0, sim, 1e-6)

	cosDist, err := vector.CosineDistance(v1, v2)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, cosDist, 1e-6)

	// 欧几里得 L2 距离：sqrt(1^2 + 1^2) = sqrt(2) ≈ 1.41421356
	l2, err := vector.L2Distance(v1, v2)
	require.NoError(t, err)
	assert.InDelta(t, math.Sqrt2, l2, 1e-5)

	// 曼哈顿 L1 距离：|1 - 0| + |0 - 1| = 2.0
	l1, err := vector.L1Distance(v1, v2)
	require.NoError(t, err)
	assert.InDelta(t, 2.0, l1, 1e-5)

	// 模长与归一化
	v3 := vector.NewVector([]float32{3.0, 4.0})
	norm := vector.Norm(v3)
	assert.InDelta(t, 5.0, norm, 1e-6)

	normalized, err := vector.Normalize(v3)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, vector.Norm(normalized), 1e-6)
	assert.InDelta(t, 0.6, float64(normalized.Slice()[0]), 1e-5)
	assert.InDelta(t, 0.8, float64(normalized.Slice()[1]), 1e-5)

	// 异常边界测试
	var empty vector.Vector
	assert.InDelta(t, 0.0, vector.Norm(empty), 1e-6)

	_, err = vector.Normalize(empty)
	require.ErrorIs(t, err, vector.ErrEmptyVector)

	_, err = vector.Normalize(vector.NewVector([]float32{0.0, 0.0}))
	require.ErrorIs(t, err, vector.ErrZeroNorm)

	// 维度不一致错误
	_, err = vector.DotProduct(v1, v3)
	require.ErrorIs(t, err, vector.ErrDimensionMismatch)
	_, err = vector.L2Distance(v1, v3)
	require.ErrorIs(t, err, vector.ErrDimensionMismatch)
	_, err = vector.L1Distance(v1, v3)
	require.ErrorIs(t, err, vector.ErrDimensionMismatch)
}

// TestVector_BunQuery 验证针对 Bun ORM SelectQuery 渲染生成的 pgvector SQL 表达式。
func TestVector_BunQuery(t *testing.T) {
	db := bun.NewDB(&sql.DB{}, pgdialect.New())

	queryVec := vector.NewVector([]float32{0.1, 0.2, 0.3})

	type Item struct {
		ID        int64         `bun:"id,pk"`
		Embedding vector.Vector `bun:"embedding"`
	}

	// 1. OrderByDistance (Top-K ANN 搜索)
	q1 := db.NewSelect().Model((*Item)(nil))
	vector.OrderByDistance(q1, "embedding", queryVec, vector.MetricCosine, true)
	sqlStr1 := q1.String()
	assert.Contains(t, sqlStr1, `("embedding" <=> '[0.1,0.2,0.3]') ASC`)

	// 1.1 验证自解释排序别名辅助函数
	q1Asc := db.NewSelect().Model((*Item)(nil))
	vector.OrderByDistanceAsc(q1Asc, "embedding", queryVec, vector.MetricCosine)
	assert.Contains(t, q1Asc.String(), `("embedding" <=> '[0.1,0.2,0.3]') ASC`)

	q1Desc := db.NewSelect().Model((*Item)(nil))
	vector.OrderByDistanceDesc(q1Desc, "embedding", queryVec, vector.MetricCosine)
	assert.Contains(t, q1Desc.String(), `("embedding" <=> '[0.1,0.2,0.3]') DESC`)

	assert.Nil(t, vector.OrderByDistanceAsc(nil, "embedding", queryVec, vector.MetricCosine))
	assert.Nil(t, vector.OrderByDistanceDesc(nil, "embedding", queryVec, vector.MetricCosine))

	// 2. WhereDistanceWithin 距离阈值截断
	q2 := db.NewSelect().Model((*Item)(nil))
	vector.WhereDistanceWithin(q2, "embedding", queryVec, vector.MetricL2, 0.5)
	sqlStr2 := q2.String()
	assert.Contains(t, sqlStr2, `("embedding" <-> '[0.1,0.2,0.3]') <= 0.5`)

	// 3. ColumnDistance 投影列
	q3 := db.NewSelect().Model((*Item)(nil))
	vector.ColumnDistance(q3, "embedding", queryVec, vector.MetricInnerProduct, "dist_score")
	sqlStr3 := q3.String()
	assert.Contains(t, sqlStr3, `("embedding" <#> '[0.1,0.2,0.3]') AS "dist_score"`)

	// 4. ColumnSimilarity 相似度得分投影
	q4 := db.NewSelect().Model((*Item)(nil))
	vector.ColumnSimilarity(q4, "embedding", queryVec, "similarity")
	sqlStr4 := q4.String()
	assert.Contains(t, sqlStr4, `(1 - ("embedding" <=> '[0.1,0.2,0.3]')) AS "similarity"`)

	// 5. 边界保护：nil query 应当安全返回 nil
	assert.Nil(t, vector.OrderByDistance(nil, "embedding", queryVec, vector.MetricCosine, true))
	assert.Nil(t, vector.WhereDistanceWithin(nil, "embedding", queryVec, vector.MetricCosine, 0.5))
	assert.Nil(t, vector.ColumnDistance(nil, "embedding", queryVec, vector.MetricCosine, "score"))
	assert.Nil(t, vector.ColumnSimilarity(nil, "embedding", queryVec, "score"))
}

// TestVector_BinaryAndIsZero 验证二进制编码/解码与 IsZero 判定。
func TestVector_BinaryAndIsZero(t *testing.T) {
	v := vector.NewVector([]float32{1.0, 2.5, -3.25})
	assert.False(t, v.IsZero())

	var empty vector.Vector
	assert.True(t, empty.IsZero())

	// 二进制编码
	buf, err := v.EncodeBinary(nil)
	require.NoError(t, err)
	assert.NotEmpty(t, buf)

	// 二进制解码
	var decoded vector.Vector
	err = decoded.DecodeBinary(buf)
	require.NoError(t, err)
	assert.Equal(t, 3, decoded.Dim())
	assert.Equal(t, []float32{1.0, 2.5, -3.25}, decoded.Slice())
}

// 确保符合 sql.Scanner, driver.Valuer, json.Marshaler, json.Unmarshaler 契约的编译期检查
var (
	_ sql.Scanner      = (*vector.Vector)(nil)
	_ driver.Valuer    = (*vector.Vector)(nil)
	_ json.Marshaler   = (*vector.Vector)(nil)
	_ json.Unmarshaler = (*vector.Vector)(nil)
)

func BenchmarkCosineSimilarity(b *testing.B) {
	v1 := vector.NewVector(make([]float32, 1536))
	v2 := vector.NewVector(make([]float32, 1536))
	s1 := v1.Slice()
	s2 := v2.Slice()
	for i := range s1 {
		s1[i] = float32(i) * 0.001
		s2[i] = float32(i) * 0.002
	}
	v1 = vector.NewVector(s1)
	v2 = vector.NewVector(s2)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = vector.CosineSimilarity(v1, v2)
	}
}

func BenchmarkNewVectorFromFloat64(b *testing.B) {
	floats := make([]float64, 1536)
	for i := range floats {
		floats[i] = float64(i) * 0.001
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = vector.NewVectorFromFloat64(floats)
	}
}

func BenchmarkVector_At(b *testing.B) {
	v := vector.NewVector(make([]float32, 1536))
	b.ResetTimer()
	b.ReportAllocs()
	var sum float32
	for b.Loop() {
		sum += v.At(500)
	}
	_ = sum
}

func BenchmarkVector_Get(b *testing.B) {
	v := vector.NewVector(make([]float32, 1536))
	b.ResetTimer()
	b.ReportAllocs()
	var sum float32
	for b.Loop() {
		if val, ok := v.Get(500); ok {
			sum += val
		}
	}
	_ = sum
}
