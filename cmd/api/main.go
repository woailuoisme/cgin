// Package main 是 bgin 的唯一入口，只负责把控制权交给命令行层。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"go.uber.org/zap"

	"bgin/internal/console"
	"bgin/internal/platform/logger"
)

// 构建元信息，通过 -ldflags "-X main.version=..." 注入。
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// 通用文档信息，由 swag 解析生成 OpenAPI 规范（见 justfile 的 docs 任务）。
// 完整契约——响应结构、错误模型、分页、限流与版本演进——见仓库根目录的 api.md。
//
// 注意：@BasePath 是 /v1，因此各 handler 上的 @Router 路径均不含 /v1 前缀。
// /livez 与 /readyz 是编排系统的存活与就绪探针，不在 /v1 之下，故不进入本文档。
//
// @title                      bgin API
// @version                    1.0
// @description                基于 Better-Auth 端点命名的认证与用户管理接口。
// @description                成功响应直接返回业务对象本身，只有分页列表带 items/meta 信封。
// @description                错误响应统一为 {code, message}，校验失败时附加按字段路径归集的 errors。
// @BasePath                   /v1
// @tag.name                   认证中心
// @tag.description            账号注册、登录会话、令牌续签与密码重置接口
// @tag.x-order                1
// @tag.name                   个人资料
// @tag.description            当前登录用户本人资料读取与局部更新接口
// @tag.x-order                2
// @tag.name                   实时通信
// @tag.description            Centrifugo 实时通信连接凭据与订阅鉴权代理端点
// @tag.x-order                3
// @securityDefinitions.access_token BearerAuth
// @in                         header
// @name                       Authorization
// @description                取值为 "Bearer <access_token>"，含 Bearer 前缀与一个空格。
func main() {
	console.SetVersion(version, commit, date)

	if err := console.Execute(context.Background(), os.Args); err != nil {
		// 用户在交互式控制台中主动选择取消属于正常意图分支，静默以退出码 0 退出，避免产生虚假错误日志
		if errors.Is(err, console.ErrCanceled) {
			os.Exit(0)
		}

		log := logger.Default()
		// 通过结构化日志记录致命退出事件（生产 JSON 模式下供 APM/Loki 搜集）
		log.Error("process terminated with error", zap.Error(err))
		_ = log.Sync()

		// 在开发控制台终端下输出高亮错误及 oops 调用堆栈，方便开发者直观排查
		if log.Core().Enabled(zap.DebugLevel) {
			_, _ = fmt.Fprintf(os.Stderr, "\n\033[1;31m[FATAL ERROR]\033[0m %+v\n\n", err)
		} else {
			_, _ = fmt.Fprintf(os.Stderr, "bgin: %v\n", err)
		}
		os.Exit(1)
	}
}
