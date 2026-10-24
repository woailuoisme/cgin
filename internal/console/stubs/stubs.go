// Package stubs 提供内嵌的代码生成模板文件。
package stubs

import "embed"

// FS 内嵌全部代码生成模板（*.tmpl）。
//
//go:embed *.tmpl
var FS embed.FS
