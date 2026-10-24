// Package main 是 bgin 的唯一入口。
// 本文件声明管理端 OpenAPI 文档元信息，由 swag 解析生成后台规范（见 justfile 的 docs 任务）。
package main

// 通用管理端文档信息，由 swag 解析生成管理端 OpenAPI 规范。
// 完整契约——响应结构、错误模型、分页、限流与版本演进——见仓库根目录的 api.md。
//
// 注意：@BasePath 是 /v1，因此各 handler 上的 @Router 路径均不含 /v1 前缀。
//
// @title                      bgin Admin API
// @version                    1.0
// @description                bgin 系统的后台管理 API 接口。
// @description                成功响应直接返回业务对象本身，只有分页列表带 items/meta 信封。
// @description                错误响应统一为 {code, message}，校验失败时附加按字段路径归集的 errors。
// @BasePath                   /v1
// @tag.name                   管理员认证
// @tag.description            后台管理员凭据登录、当前会话获取、令牌续签与密码修改等管理接口
// @tag.x-order                1
// @tag.name                   用户管理
// @tag.description            系统用户查询、创建、编辑与注销等管理接口
// @tag.x-order                2
// @securityDefinitions.access_token BearerAuth
// @in                         header
// @name                       Authorization
// @description                取值为 "Bearer <access_token>"，含 Bearer 前缀与一个空格（要求具有管理员角色）。
