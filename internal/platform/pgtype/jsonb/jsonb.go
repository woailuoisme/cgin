package jsonb

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// ErrInvalidJSONB 表示非法的 JSONB 格式或无法反序列化。
var ErrInvalidJSONB = errors.New("jsonb: invalid json format")

// JSONB 是针对 PostgreSQL jsonb 列的强类型泛型容器。
// 既能享受编译期强类型安全，又能在底层与 PostgreSQL jsonb 二进制及 GIN 索引无缝交互。
type JSONB[T any] struct {
	Data T
}

// New 构造包含指定数据的 JSONB 容器。
func New[T any](data T) JSONB[T] {
	return JSONB[T]{Data: data}
}

// Get 获取底层强类型数据。
func (j JSONB[T]) Get() T {
	return j.Data
}

// Set 设置底层数据。
func (j *JSONB[T]) Set(data T) {
	j.Data = data
}

// IsZero 判定底层数据是否为 Go 的零值或空。
// 优先走 IsZero 契约与快速指针判断，消除热路径上 reflect.DeepEqual 的堆逃逸与 CPU 开销。
func (j JSONB[T]) IsZero() bool {
	var anyData any = j.Data
	if anyData == nil {
		return true
	}
	if z, ok := anyData.(interface{ IsZero() bool }); ok {
		return z.IsZero()
	}
	val := reflect.ValueOf(anyData)
	switch val.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface, reflect.Func, reflect.Chan:
		return val.IsNil()
	default:
		return val.IsZero()
	}
}

// String 返回序列化后的 JSON 字符串；若序列化失败则返回空字符串。
func (j JSONB[T]) String() string {
	b, err := json.Marshal(j.Data)
	if err != nil {
		return ""
	}
	return string(b)
}

// Value 实现 driver.Valuer 接口，将泛型数据序列化为 JSON 字符串写入 PostgreSQL。
//
//nolint:nilnil // driver.Valuer returns (nil, nil) for SQL NULL
func (j JSONB[T]) Value() (driver.Value, error) {
	var anyData any = j.Data
	if anyData == nil {
		return nil, nil
	}
	val := reflect.ValueOf(anyData)
	switch val.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map:
		if val.IsNil() {
			return nil, nil
		}
	}
	bytesData, err := json.Marshal(j.Data)
	if err != nil {
		return nil, fmt.Errorf("jsonb: marshal failed: %w", err)
	}
	return string(bytesData), nil
}

// Scan 实现 sql.Scanner 接口，从 PostgreSQL jsonb 字段反序列化到泛型容器。
func (j *JSONB[T]) Scan(src any) error {
	if src == nil {
		var zero T
		j.Data = zero
		return nil
	}

	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("%w: unsupported type %T", ErrInvalidJSONB, src)
	}

	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		var zero T
		j.Data = zero
		return nil
	}

	if err := json.Unmarshal(raw, &j.Data); err != nil {
		return fmt.Errorf("%w: unmarshal failed: %w", ErrInvalidJSONB, err)
	}
	return nil
}

// MarshalJSON 实现 json.Marshaler 接口，透明输出底层数据的 JSON。
func (j JSONB[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal(j.Data)
}

// UnmarshalJSON 实现 json.Unmarshaler 接口，将传入的 JSON 解析到底层数据。
func (j *JSONB[T]) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		var zero T
		j.Data = zero
		return nil
	}
	return json.Unmarshal(data, &j.Data)
}
