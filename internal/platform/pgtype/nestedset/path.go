package nestedset

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	// labelPattern 规定每个路径标签必须由 1-255 位字母、数字或下划线组成。
	labelPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)

	// ErrInvalidPath 表示路径格式不符合 PostgreSQL ltree 语法规范。
	ErrInvalidPath = errors.New("nestedset: invalid ltree path format")
	// ErrInvalidLabel 表示标签包含非法字符或超长。
	ErrInvalidLabel = errors.New("nestedset: invalid label format, must match ^[A-Za-z0-9_]{1,255}$")
)

// Path 表示 PostgreSQL ltree 点分标签路径（例如 "Top.Science.Astronomy"）。
type Path string

// LTree 为 Path 的语义别名，强化与 PostgreSQL 原生扩展的心智对齐。
type LTree = Path

// ValidateLabel 校验单个路径标签名称是否合法。
func ValidateLabel(label string) error {
	if !labelPattern.MatchString(label) {
		return fmt.Errorf("%w: %q", ErrInvalidLabel, label)
	}
	return nil
}

// NewPath 将多个标签组合构造为规范的点分路径，并执行字符校验。
func NewPath(segments ...string) (Path, error) {
	if len(segments) == 0 {
		return Path(""), nil
	}
	for _, seg := range segments {
		if err := ValidateLabel(seg); err != nil {
			return Path(""), err
		}
	}
	return Path(strings.Join(segments, ".")), nil
}

// MustPath 构造点分路径，若格式非法则 panic（通常用于常量与单元测试）。
func MustPath(segments ...string) Path {
	p, err := NewPath(segments...)
	if err != nil {
		panic(err)
	}
	return p
}

// Parse 解析点分路径字符串，若格式非法则返回错误；空字符串解析为空路径。
func Parse(s string) (Path, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Path(""), nil
	}
	parts := strings.Split(s, ".")
	return NewPath(parts...)
}

// IsValid 检查当前路径格式是否合法。
func (p Path) IsValid() bool {
	if p.IsEmpty() {
		return true
	}
	for seg := range strings.SplitSeq(string(p), ".") {
		if !labelPattern.MatchString(seg) {
			return false
		}
	}
	return true
}

// IsEmpty 判定当前路径是否为空。
func (p Path) IsEmpty() bool {
	return len(p) == 0
}

// String 返回路径字符串。
func (p Path) String() string {
	return string(p)
}

// Segments 将路径按点号解构成标签切片；若为空路径返回 nil。
func (p Path) Segments() []string {
	if p.IsEmpty() {
		return nil
	}
	return strings.Split(string(p), ".")
}

// Depth 返回路径的层级深度（段数）。
func (p Path) Depth() int {
	if p.IsEmpty() {
		return 0
	}
	return len(p.Segments())
}

// NLevel 是 Depth 的别名，对应 PostgreSQL ltree 的 nlevel() 函数。
func (p Path) NLevel() int {
	return p.Depth()
}

// Root 获取顶级根节点标签名；空路径返回空字符串。
func (p Path) Root() string {
	segs := p.Segments()
	if len(segs) == 0 {
		return ""
	}
	return segs[0]
}

// Leaf 获取当前最底层叶子节点标签名；空路径返回空字符串。
func (p Path) Leaf() string {
	segs := p.Segments()
	if len(segs) == 0 {
		return ""
	}
	return segs[len(segs)-1]
}

// Parent 获取父级路径；若当前是一级节点或空路径，则返回空路径。
func (p Path) Parent() Path {
	segs := p.Segments()
	if len(segs) <= 1 {
		return Path("")
	}
	return Path(strings.Join(segs[:len(segs)-1], "."))
}

// Child 在当前路径下派生子节点路径。
func (p Path) Child(label string) (Path, error) {
	if err := ValidateLabel(label); err != nil {
		return Path(""), err
	}
	if p.IsEmpty() {
		return Path(label), nil
	}
	return Path(string(p) + "." + label), nil
}

// Contains 判断当前路径是否为 other 的祖先或自身（对应 PostgreSQL ltree 算子 @>）。
func (p Path) Contains(other Path) bool {
	if p == other {
		return true
	}
	if p.IsEmpty() {
		return true
	}
	prefix := string(p) + "."
	return strings.HasPrefix(string(other), prefix)
}

// IsAncestorOf 判断当前路径是否为 other 的严格祖先（即包含 other 且深度小于 other）。
func (p Path) IsAncestorOf(other Path) bool {
	return p != other && p.Contains(other)
}

// IsDescendantOf 判断当前路径是否为 other 的严格后裔（即位于 other 之下且深度大于 other）。
func (p Path) IsDescendantOf(other Path) bool {
	return p != other && other.Contains(p)
}

// Subpath 截取子路径，对应 PostgreSQL ltree 的 subpath(path, offset, len)。
// offset 支持负索引（-1 表示最后一个元素）。若 len <= 0 则截取至末尾。
func (p Path) Subpath(offset, length int) Path {
	segs := p.Segments()
	n := len(segs)
	if n == 0 {
		return Path("")
	}

	start := offset
	if start < 0 {
		start = n + start
	}
	if start < 0 {
		start = 0
	}
	if start >= n {
		return Path("")
	}

	end := n
	if length > 0 {
		end = min(start+length, n)
	}

	return Path(strings.Join(segs[start:end], "."))
}

// LCA 计算一组路径的最低公共祖先（Lowest Common Ancestor）。
func LCA(paths ...Path) Path {
	if len(paths) == 0 {
		return Path("")
	}
	if len(paths) == 1 {
		return paths[0]
	}

	common := paths[0].Segments()
	for _, p := range paths[1:] {
		segs := p.Segments()
		minLen := min(len(common), len(segs))
		matchLen := 0
		for i := range minLen {
			if common[i] == segs[i] {
				matchLen++
			} else {
				break
			}
		}
		common = common[:matchLen]
		if len(common) == 0 {
			break
		}
	}

	if len(common) == 0 {
		return Path("")
	}
	return Path(strings.Join(common, "."))
}

// Value 实现 driver.Valuer 接口，写入 PostgreSQL ltree 字段。
//
//nolint:nilnil // driver.Valuer returns (nil, nil) for SQL NULL
func (p Path) Value() (driver.Value, error) {
	if p.IsEmpty() {
		return nil, nil
	}
	return string(p), nil
}

// Scan 实现 sql.Scanner 接口，从 PostgreSQL ltree 列解析。
func (p *Path) Scan(src any) error {
	if src == nil {
		*p = Path("")
		return nil
	}
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("nestedset: cannot scan %T into Path", src)
	}
	parsed, err := Parse(s)
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}

// MarshalJSON 实现 json.Marshaler 接口，序列化为字符串。
func (p Path) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(p))
}

// UnmarshalJSON 实现 json.Unmarshaler 接口，宽容支持字符串或字符串数组反序列化。
func (p *Path) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*p = Path("")
		return nil
	}

	// 1. 尝试解析为字符串
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		parsed, parseErr := Parse(str)
		if parseErr != nil {
			return parseErr
		}
		*p = parsed
		return nil
	}

	// 2. 尝试解析为字符串数组 (如 ["Top", "Science"])
	var segs []string
	if err := json.Unmarshal(data, &segs); err == nil {
		parsed, parseErr := NewPath(segs...)
		if parseErr != nil {
			return parseErr
		}
		*p = parsed
		return nil
	}

	return ErrInvalidPath
}
