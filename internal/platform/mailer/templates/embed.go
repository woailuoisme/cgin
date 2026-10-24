package templates

import "embed"

// FS 导出内嵌的邮件模板文件系统。
//
//go:embed layouts/*.tmpl components/*.tmpl *.tmpl
var FS embed.FS
