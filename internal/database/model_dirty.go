package database

import (
	"reflect"
	"slices"
	"strings"
)

// DirtyTrackable 约束支持变更追踪与脏检查的实体模型（对标 Laravel isDirty/getDirty）。
type DirtyTrackable interface {
	Model
	SyncOriginal()
	GetDirty() []string
	IsDirty(columns ...string) bool
}

// ChangeTracker 提供轻量、非侵入式的实体属性快照与脏检查追踪器。
type ChangeTracker struct {
	original map[string]any
}

// structFields 从目标结构体（或结构体指针）提取所有带 bun tag 的有效列字段键值映射。
func structFields(model any) (map[string]any, bool) {
	if model == nil {
		return nil, false
	}
	val := reflect.ValueOf(model)
	if val.Kind() == reflect.Pointer {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return nil, false
	}
	fields := make(map[string]any, val.NumField())
	extractFields(val, val.Type(), fields)
	return fields, true
}

// SyncOriginalFrom 捕获目标实体当前所有带 bun tag 的字段值作为基准快照。
func (ct *ChangeTracker) SyncOriginalFrom(model any) {
	if fields, ok := structFields(model); ok {
		ct.original = fields
	}
}

// parseBunColumn 从 bun tag 中提取有效列名。
func parseBunColumn(tag string) (string, bool) {
	if tag == "" || tag == "-" {
		return "", false
	}
	col, _, _ := strings.Cut(tag, ",")
	if col == "" || strings.HasPrefix(col, "table:") || strings.HasPrefix(col, "rel:") {
		return "", false
	}
	return col, true
}

// extractFields 递归遍历结构体导出字段，提取 bun 标签列名与当前值。
func extractFields(val reflect.Value, typ reflect.Type, dest map[string]any) {
	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			extractFields(val.Field(i), field.Type, dest)
			continue
		}
		if colName, ok := parseBunColumn(field.Tag.Get("bun")); ok {
			dest[colName] = val.Field(i).Interface()
		}
	}
}

// GetDirtyWith 对比当前实体与基准快照，返回所有发生改变的列名切片。
func (ct *ChangeTracker) GetDirtyWith(model any) []string {
	if ct.original == nil {
		return nil
	}
	current, ok := structFields(model)
	if !ok {
		return nil
	}

	dirty := make([]string, 0, len(current))
	for col, oldVal := range ct.original {
		newVal, exists := current[col]
		if !exists || !reflect.DeepEqual(oldVal, newVal) {
			dirty = append(dirty, col)
		}
	}
	return dirty
}

// IsDirtyWith 检查指定列（若未传列名则检查任意列）是否发生改变。
func (ct *ChangeTracker) IsDirtyWith(model any, columns ...string) bool {
	dirty := ct.GetDirtyWith(model)
	if len(dirty) == 0 {
		return false
	}
	if len(columns) == 0 {
		return true
	}
	for _, col := range columns {
		if slices.Contains(dirty, col) {
			return true
		}
	}
	return false
}
