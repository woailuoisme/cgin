// Package opt 提供零外部依赖、强类型、开箱即用的 JSON 三态字段容器 (Field[T])。
//
// 专为 PATCH 局部更新场景设计，精准区分三种语义：
//  1. 未传 (Unset)：字段未在 JSON 中出现，结构体零值天然为未传状态；
//  2. 显式置空 (Null)：字段在 JSON 中以 "key": null 形式传递，代表数据库需置空/清除意图；
//  3. 赋值 (Value)：字段在 JSON 中以 "key": "value" 形式传递，代表数据库需更新为新值。
package opt

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type state byte

const (
	stateUnset state = iota // 0: 未传 (结构体零值天然为未传)
	stateNull               // 1: 显式置空
	stateSet                // 2: 具有具体值
)

// Field 封装具备三态语义的强类型值对象。
type Field[T any] struct {
	val   T
	state state
}

// From 构造包含具体值的 Field[T]。
func From[T any](val T) Field[T] {
	return Field[T]{
		val:   val,
		state: stateSet,
	}
}

// Null 构造显式置空的 Field[T]。
func Null[T any]() Field[T] {
	return Field[T]{
		state: stateNull,
	}
}

// FromPtr 根据指针构造 Field[T]：若 ptr 为 nil 则返回 Null 状态，否则包装其解引用值。
func FromPtr[T any](ptr *T) Field[T] {
	if ptr == nil {
		return Null[T]()
	}
	return From(*ptr)
}

// IsUnset 报告字段是否在输入 JSON 中完全缺失。
func (f Field[T]) IsUnset() bool {
	return f.state == stateUnset
}

// IsNull 报告字段是否被显式传递为 null。
func (f Field[T]) IsNull() bool {
	return f.state == stateNull
}

// IsValue 报告字段是否包含具体有效值。
func (f Field[T]) IsValue() bool {
	return f.state == stateSet
}

// IsSet 报告字段是否在输入中显式出现（无论为 null 还是具体值）。
func (f Field[T]) IsSet() bool {
	return f.state != stateUnset
}

// Value 返回底层具体值；若字段不包含值则返回零值。
func (f Field[T]) Value() T {
	return f.val
}

// Get 尝试读取有效值；若为 Value 状态返回 (val, true)，否则返回 (zero, false)。
func (f Field[T]) Get() (T, bool) {
	if f.state == stateSet {
		return f.val, true
	}
	var zero T
	return zero, false
}

// MustGet 读取有效值；若不为 Value 状态则抛出 panic。
func (f Field[T]) MustGet() T {
	if f.state != stateSet {
		panic(fmt.Sprintf("opt: Field[%T] does not contain a value (state: %d)", f.val, f.state))
	}
	return f.val
}

// Ptr 返回指向底层值的指针；若为 Unset 或 Null 则返回 nil。
func (f Field[T]) Ptr() *T {
	if f.state == stateSet {
		v := f.val
		return &v
	}
	return nil
}

// Or 当字段具有具体值时返回该值，否则返回 fallback 兜底默认值。
func (f Field[T]) Or(fallback T) T {
	if f.state == stateSet {
		return f.val
	}
	return fallback
}

// UnmarshalJSON 实现 json.Unmarshaler 契约。
// 仅当 JSON 键存在时该方法才会被标准库触发：
// 若数据为 "null"，置为 stateNull；否则反序列化具体值并置为 stateSet。
func (f *Field[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		f.state = stateNull
		var zero T
		f.val = zero
		return nil
	}
	if err := json.Unmarshal(data, &f.val); err != nil {
		return err
	}
	f.state = stateSet
	return nil
}

// MarshalJSON 实现 json.Marshaler 契约。
func (f Field[T]) MarshalJSON() ([]byte, error) {
	switch f.state {
	case stateSet:
		return json.Marshal(f.val)
	case stateNull:
		return []byte("null"), nil
	default:
		return []byte("null"), nil
	}
}
