// Package apidocs 只做一件事：把 swag 生成的前后台两套 OpenAPI 规范编进二进制，
// 供 /docs/openapi.json 与 /docs/admin/openapi.json 端点原样返回。
//
// 本目录下只有 embed.go 是手写的，swagger.json 与 admin_swagger.json 由 `just docs` 生成，且不入 .gitignore——
//
// 这里刻意不使用 swag 生成的 docs.go：那会把 github.com/swaggo/swag 拖进运行时依赖，
// 而它靠注册表机制提供规范，要求 UI 端与生成端版本严格配对（swag v2 与 gin-swagger v1
// 就不互通）。直接托管静态规范没有配对问题，前端 UI 也可以自由更换。
package apidocs

import _ "embed"

// Spec 是客户端 API 规范原文，格式为 Swagger 2.0。
//
//go:embed swagger.json
var Spec []byte

// AdminSpec 是管理端 API 规范原文，格式为 Swagger 2.0。
//
//go:embed admin_swagger.json
var AdminSpec []byte
