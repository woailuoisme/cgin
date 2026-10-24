package vector

import (
	"errors"
	"fmt"
	"math"
)

var (
	// ErrEmptyVector 表示操作遇到了空向量。
	ErrEmptyVector = errors.New("vector: empty vector")
	// ErrDimensionMismatch 表示两个向量的维度不一致。
	ErrDimensionMismatch = errors.New("vector: dimension mismatch")
	// ErrZeroNorm 表示向量模长为零，无法执行归一化或余弦度量。
	ErrZeroNorm = errors.New("vector: zero norm vector cannot be normalized or measured")
)

// Norm 计算向量的 L2 欧几里得模长 (Norm/Magnitude)。
func Norm(v Vector) float64 {
	s := v.rawSlice()
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, val := range s {
		f := float64(val)
		sum += f * f
	}
	return math.Sqrt(sum)
}

// Normalize 将向量转化为单位模长的单位向量。
func Normalize(v Vector) (Vector, error) {
	s := v.rawSlice()
	if len(s) == 0 {
		return Vector{}, ErrEmptyVector
	}
	norm := Norm(v)
	if norm == 0 {
		return Vector{}, ErrZeroNorm
	}
	res := make([]float32, len(s))
	for i, val := range s {
		res[i] = float32(float64(val) / norm)
	}
	return newVectorNoClone(res), nil
}

// DotProduct 计算两个向量的代数点积（内积）。
func DotProduct(a, b Vector) (float64, error) {
	sa, sb := a.rawSlice(), b.rawSlice()
	if len(sa) == 0 || len(sb) == 0 {
		return 0, ErrEmptyVector
	}
	if len(sa) != len(sb) {
		return 0, fmt.Errorf("%w: %d vs %d", ErrDimensionMismatch, len(sa), len(sb))
	}
	var sum float64
	for i := range sa {
		sum += float64(sa[i]) * float64(sb[i])
	}
	return sum, nil
}

// CosineSimilarity 计算两向量的余弦相似度，值域通常位于 [-1.0, 1.0]。
func CosineSimilarity(a, b Vector) (float64, error) {
	dot, err := DotProduct(a, b)
	if err != nil {
		return 0, err
	}
	normA := Norm(a)
	normB := Norm(b)
	if normA == 0 || normB == 0 {
		return 0, ErrZeroNorm
	}
	sim := dot / (normA * normB)
	// 消除浮点数计算微小越界
	if sim > 1.0 {
		sim = 1.0
	} else if sim < -1.0 {
		sim = -1.0
	}
	return sim, nil
}

// CosineDistance 计算两向量的余弦距离，即 1.0 - CosineSimilarity。
func CosineDistance(a, b Vector) (float64, error) {
	sim, err := CosineSimilarity(a, b)
	if err != nil {
		return 0, err
	}
	return 1.0 - sim, nil
}

// L2Distance 计算两向量之间的欧几里得距离（L2 空间直线距离）。
func L2Distance(a, b Vector) (float64, error) {
	sa, sb := a.rawSlice(), b.rawSlice()
	if len(sa) == 0 || len(sb) == 0 {
		return 0, ErrEmptyVector
	}
	if len(sa) != len(sb) {
		return 0, fmt.Errorf("%w: %d vs %d", ErrDimensionMismatch, len(sa), len(sb))
	}
	var sum float64
	for i := range sa {
		diff := float64(sa[i]) - float64(sb[i])
		sum += diff * diff
	}
	return math.Sqrt(sum), nil
}

// L1Distance 计算两向量之间的曼哈顿距离（L1 坐标轴差绝对值总和）。
func L1Distance(a, b Vector) (float64, error) {
	sa, sb := a.rawSlice(), b.rawSlice()
	if len(sa) == 0 || len(sb) == 0 {
		return 0, ErrEmptyVector
	}
	if len(sa) != len(sb) {
		return 0, fmt.Errorf("%w: %d vs %d", ErrDimensionMismatch, len(sa), len(sb))
	}
	var sum float64
	for i := range sa {
		sum += math.Abs(float64(sa[i]) - float64(sb[i]))
	}
	return sum, nil
}
