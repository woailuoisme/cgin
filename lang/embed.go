// Package lang 只做一件事：把多语言资源编译进二进制。
// 资源文件不参与业务逻辑，语言包的实际装载在 internal/platform/i18n。
package lang

import "embed"

// FS 内嵌 en 与 zh-CN 两门语言的文案。
//
//go:embed en zh-CN
var FS embed.FS
